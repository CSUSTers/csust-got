use super::super::deferred::DeferredCommandCleanup;
use super::super::supervisor::CommandSupervisor;
use super::super::*;

pub(super) struct PreparedProductionCommand {
    pub(super) child: Child,
    pub(super) group: CommandCgroup,
    pub(super) cgroup_name: String,
    pub(super) cpu_budget: Duration,
    pub(super) startup_status: ExecStartupStatusReader,
    pub(super) lifecycle: CommandLifecycleLease,
}

pub(super) fn prepare<F>(
    supervisor: &CommandSupervisor,
    cgroups: &CgroupManager,
    exec_helper: &Path,
    rlimits: &RlimitSpec,
    identity: &CommandIdentity,
    target: ExecTarget,
    launch_factory: &mut Option<F>,
    timeout: Duration,
    cleanup_dir: Option<PathBuf>,
    #[cfg(feature = "c7-test-support")] spawn_controls: &SpawnControls,
) -> Result<PreparedProductionCommand, SupervisorError>
where
    F: FnOnce() -> Result<CommandLaunch, String>,
{
    let group = cgroups.create(identity).map_err(|_| {
        supervisor.inner.health.latch_enforcement_failure();
        SupervisorError::Unavailable(supervisor.inner.health.reason())
    })?;
    #[cfg(feature = "c7-test-support")]
    if spawn_controls.fixture_cpu_stat
        && std::fs::write(group.path().join("cpu.stat"), "usage_usec 0\n").is_err()
    {
        supervisor.inner.health.latch_enforcement_failure();
        let _ = group.kill_wait_remove_blocking();
        return Err(SupervisorError::Unavailable(
            supervisor.inner.health.reason(),
        ));
    }
    let cgroup_name = group.name().to_string();
    let cpu_budget = cgroups.limits().cpu_budget.min(timeout);
    let launch = match launch_factory
        .take()
        .expect("launch factory is consumed once")()
    {
        Ok(launch) => launch,
        Err(error) => {
            let cleanup_error = group.kill_wait_remove_blocking().err();
            if cleanup_error.is_some() {
                supervisor.inner.health.latch_cleanup_failure();
            }
            let message = match cleanup_error {
                Some(cleanup_error) => format!(
                    "prepare command environment failed: {error}; cgroup cleanup failed: {cleanup_error}"
                ),
                None => format!("prepare command environment failed: {error}"),
            };
            return Err(SupervisorError::Spawn(message));
        }
    };
    if let Err(error) = validate_environment(&launch.environment) {
        match revoke_before_cgroup_cleanup(&launch.lifecycle, &supervisor.inner.health) {
            Ok(()) => {
                let cleanup_error = group.kill_wait_remove_blocking().err();
                if cleanup_error.is_some() {
                    supervisor.inner.health.latch_cleanup_failure();
                }
                let mut failures = vec![error.to_string()];
                if let Some(cleanup_error) = cleanup_error {
                    failures.push(format!("cgroup cleanup failed: {cleanup_error}"));
                }
                return Err(SupervisorError::Spawn(failures.join("; ")));
            }
            Err(revoke_error) => {
                supervisor
                    .inner
                    .deferred
                    .retain(DeferredCommandCleanup::new_unlaunched(
                        Some(group),
                        cleanup_dir,
                        None,
                    ));
                supervisor.inner.health.latch_cleanup_failure();
                return Err(SupervisorError::CleanupDeferred(format!(
                    "command binding cleanup failed: {revoke_error}"
                )));
            }
        }
    }
    let lifecycle = launch.lifecycle;
    let spec = ExecSpec {
        cgroup_procs: group.procs_path().to_path_buf(),
        program: target.program,
        args: target.args,
        cwd: target.cwd,
        env: launch.environment,
        rlimits: rlimits.clone(),
    };
    #[cfg(not(feature = "c7-test-support"))]
    let spawn_result = spawn_exec_helper_with_control(exec_helper, &spec, launch.control_source);
    #[cfg(feature = "c7-test-support")]
    let spawn_result = spawn_exec_helper_with_control_and_controls(
        exec_helper,
        &spec,
        launch.control_source,
        spawn_controls.clone(),
    );
    match spawn_result {
        Ok(spawned) => Ok(PreparedProductionCommand {
            child: spawned.child,
            group,
            cgroup_name,
            cpu_budget,
            startup_status: spawned.status,
            lifecycle,
        }),
        Err(_) => {
            supervisor.inner.health.latch_enforcement_failure();
            match revoke_before_cgroup_cleanup(&lifecycle, &supervisor.inner.health) {
                Ok(()) => {
                    let cleanup_error = group.kill_wait_remove_blocking().err();
                    if cleanup_error.is_some() {
                        supervisor.inner.health.latch_cleanup_failure();
                    }
                    Err(SupervisorError::Unavailable(
                        supervisor.inner.health.reason(),
                    ))
                }
                Err(revoke_error) => {
                    supervisor
                        .inner
                        .deferred
                        .retain(DeferredCommandCleanup::new_unlaunched(
                            Some(group),
                            cleanup_dir,
                            None,
                        ));
                    supervisor.inner.health.latch_cleanup_failure();
                    Err(SupervisorError::CleanupDeferred(format!(
                        "command binding cleanup failed: {revoke_error}"
                    )))
                }
            }
        }
    }
}

pub(super) fn revoke_before_cgroup_cleanup(
    lifecycle: &CommandLifecycleLease,
    health: &BashHealth,
) -> Result<(), String> {
    let runtime = tokio::runtime::Handle::try_current()
        .map_err(|error| format!("runtime is unavailable for command binding cleanup: {error}"))?;
    tokio::task::block_in_place(|| {
        runtime
            .block_on(lifecycle.revoke_and_wait_with_slow_observer(|| {
                health.latch_cleanup_failure();
            }))
            .map(|_| ())
            .map_err(|error| error.to_string())
    })
}
