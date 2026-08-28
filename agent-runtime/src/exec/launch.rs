#[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
use super::supervisor::SupervisorBackend;
use super::supervisor::{CommandHandle, CommandSupervisor};
#[cfg(test)]
use super::supervisor::{DirectExecStatus, ErrorExecStatus, PendingExecStatus};
use super::*;

mod api;
mod handle;
#[cfg(target_os = "linux")]
mod production;
#[cfg(all(target_os = "linux", any(test, feature = "c7-test-support")))]
use production::revoke_before_cgroup_cleanup;

impl CommandSupervisor {
    pub fn start_command_with_launch<F>(
        &self,
        identity: CommandIdentity,
        target: ExecTarget,
        launch_factory: F,
        timeout: Duration,
        max_output_chars: usize,
        cleanup_dir: Option<PathBuf>,
    ) -> Result<CommandHandle, SupervisorError>
    where
        F: FnOnce() -> Result<CommandLaunch, String>,
    {
        #[cfg(all(not(target_os = "linux"), not(test), not(feature = "c7-test-support")))]
        {
            let _ = (
                identity,
                target,
                launch_factory,
                timeout,
                max_output_chars,
                cleanup_dir,
            );
            return Err(SupervisorError::Unavailable(
                "agent runtime production execution requires Linux".to_string(),
            ));
        }
        #[cfg(any(target_os = "linux", test, feature = "c7-test-support"))]
        {
            #[cfg(all(test, not(target_os = "linux")))]
            let _ = &identity;
            if timeout.is_zero() {
                return Err(SupervisorError::Spawn(
                    "command timeout must be positive".to_string(),
                ));
            }
            let (cancel, cancel_rx) = watch::channel(false);
            let active_command = self.inner.health.register(cancel.clone())?;
            let mut launch_factory = Some(launch_factory);
            let cgroup_name;
            let cgroup;
            let cpu_budget;
            let cpu_read_failure;
            let startup_status: ExecStartupStatusReader;
            let supervision_trace;
            let cgroup_cleanup_failure;
            let cleanup_probe;
            let lifecycle;
            let child = match &self.inner.backend {
                #[cfg(target_os = "linux")]
                SupervisorBackend::Production {
                    cgroups,
                    exec_helper,
                    rlimits,
                    #[cfg(feature = "c7-test-support")]
                    spawn_controls,
                } => {
                    let prepared = production::prepare(
                        self,
                        cgroups,
                        exec_helper,
                        rlimits,
                        &identity,
                        target,
                        &mut launch_factory,
                        timeout,
                        cleanup_dir.clone(),
                        #[cfg(feature = "c7-test-support")]
                        spawn_controls,
                    )?;
                    cgroup_name = prepared.cgroup_name;
                    cgroup = Some(prepared.group);
                    cpu_budget = Some(prepared.cpu_budget);
                    cpu_read_failure = false;
                    supervision_trace = None;
                    cgroup_cleanup_failure = false;
                    cleanup_probe = None;
                    startup_status = prepared.startup_status;
                    lifecycle = prepared.lifecycle;
                    prepared.child
                }
                #[cfg(any(test, feature = "c7-test-support"))]
                SupervisorBackend::Direct {
                    cpu_budget: test_cpu_budget,
                    faults,
                } => {
                    if faults.cgroup_create {
                        self.inner.health.latch_enforcement_failure();
                        return Err(SupervisorError::Unavailable(self.inner.health.reason()));
                    }
                    cgroup_name = String::new();
                    cgroup = None;
                    cpu_budget = *test_cpu_budget;
                    cpu_read_failure = faults.cpu_read;
                    supervision_trace = faults.trace.clone();
                    cgroup_cleanup_failure = faults.cgroup_cleanup;
                    cleanup_probe = faults.cleanup_probe.clone();
                    #[cfg(test)]
                    {
                        startup_status = match &faults.exec_status {
                            Some(DirectExecStatus::Failure(stage)) => {
                                ExecStartupStatusReader::from_stream(Box::pin(
                                    std::io::Cursor::new(
                                        encode_exec_status_failure(*stage).to_vec(),
                                    ),
                                ))
                            }
                            Some(DirectExecStatus::Payload(payload)) => {
                                ExecStartupStatusReader::from_stream(Box::pin(
                                    std::io::Cursor::new(payload.clone()),
                                ))
                            }
                            Some(DirectExecStatus::Pending) => {
                                ExecStartupStatusReader::from_stream(Box::pin(PendingExecStatus))
                            }
                            Some(DirectExecStatus::ReadError) => {
                                ExecStartupStatusReader::from_stream(Box::pin(ErrorExecStatus))
                            }
                            None => {
                                ExecStartupStatusReader::from_stream(Box::pin(tokio::io::empty()))
                            }
                        };
                    }
                    #[cfg(all(feature = "c7-test-support", not(test)))]
                    {
                        startup_status =
                            ExecStartupStatusReader::from_stream(Box::pin(tokio::io::empty()));
                    }
                    let launch = launch_factory
                        .take()
                        .expect("launch factory is consumed once")(
                    )
                    .map_err(|error| {
                        SupervisorError::Spawn(format!(
                            "prepare command environment failed: {error}"
                        ))
                    })?;
                    if let Err(error) = validate_environment(&launch.environment) {
                        #[cfg(target_os = "linux")]
                        if let Err(revoke_error) =
                            revoke_before_cgroup_cleanup(&launch.lifecycle, &self.inner.health)
                        {
                            self.inner.health.latch_cleanup_failure();
                            return Err(SupervisorError::Spawn(format!(
                                "{error}; command binding cleanup failed: {revoke_error}"
                            )));
                        }
                        return Err(SupervisorError::Spawn(error.to_string()));
                    }
                    lifecycle = launch.lifecycle;
                    #[cfg(test)]
                    if faults.helper_launch.is_some() {
                        self.inner.health.latch_enforcement_failure();
                        #[cfg(target_os = "linux")]
                        if let Err(revoke_error) =
                            revoke_before_cgroup_cleanup(&lifecycle, &self.inner.health)
                        {
                            self.inner.health.latch_cleanup_failure();
                            return Err(SupervisorError::CleanupDeferred(format!(
                                "command binding cleanup failed: {revoke_error}"
                            )));
                        }
                        return Err(SupervisorError::Unavailable(self.inner.health.reason()));
                    }
                    match spawn_direct(target, launch.environment) {
                        Ok(child) => child,
                        Err(error) => {
                            #[cfg(target_os = "linux")]
                            if let Err(revoke_error) =
                                revoke_before_cgroup_cleanup(&lifecycle, &self.inner.health)
                            {
                                self.inner.health.latch_cleanup_failure();
                                return Err(SupervisorError::Spawn(format!(
                                    "spawn command failed: {error}; command binding cleanup failed: {revoke_error}"
                                )));
                            }
                            return Err(error);
                        }
                    }
                }
            };
            let health = self.inner.health.clone();
            let deferred = self.inner.deferred.clone();
            let result = tokio::spawn(async move {
                let _active_command = active_command;
                supervision::supervise_command(
                    child,
                    cgroup,
                    cgroup_name,
                    cancel_rx,
                    timeout,
                    cpu_budget,
                    cpu_read_failure,
                    startup_status,
                    supervision_trace,
                    cgroup_cleanup_failure,
                    max_output_chars,
                    cleanup_dir,
                    lifecycle,
                    health,
                    deferred,
                    cleanup_probe,
                )
                .await
            });
            Ok(CommandHandle {
                cancel: Some(cancel),
                result: Some(result),
            })
        }
    }
}
