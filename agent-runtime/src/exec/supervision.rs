use super::deferred::{CleanupProbe, DeferredCleanupRegistry, DeferredCommandCleanup};
use super::*;
use std::sync::Mutex;

mod cleanup;
mod output;

pub(super) use cleanup::{cleanup_command_process_group, cleanup_directory};
pub(super) use output::CollectedOutput;
use output::{collect_output, join_collector};

enum CommandEnd {
    Exited(io::Result<ExitStatus>),
    WallTimeout,
    Canceled,
    CpuBudget,
    CpuRead(String),
    Enforcement,
}

#[allow(clippy::too_many_arguments)]
pub(super) async fn supervise_command(
    mut child: Child,
    mut cgroup: Option<CommandCgroup>,
    cgroup_name: String,
    mut cancel: watch::Receiver<bool>,
    wall_timeout: Duration,
    cpu_budget: Option<Duration>,
    cpu_read_failure: bool,
    mut startup_status: ExecStartupStatusReader,
    supervision_trace: Option<Arc<Mutex<Vec<&'static str>>>>,
    cgroup_cleanup_failure: bool,
    max_output_chars: usize,
    cleanup_dir: Option<PathBuf>,
    lifecycle: CommandLifecycleLease,
    health: BashHealth,
    deferred: DeferredCleanupRegistry,
    cleanup_probe: Option<Arc<CleanupProbe>>,
) -> Result<CommandOutput, SupervisorError> {
    let child_id = child.id();
    let stdout = child
        .stdout
        .take()
        .ok_or_else(|| SupervisorError::Spawn("command stdout pipe unavailable".to_string()))?;
    let stderr = child
        .stderr
        .take()
        .ok_or_else(|| SupervisorError::Spawn("command stderr pipe unavailable".to_string()))?;
    let stdout_task = tokio::spawn(collect_output(stdout, max_output_chars));
    let stderr_task = tokio::spawn(collect_output(stderr, max_output_chars));
    let started = tokio::time::Instant::now();
    let wall_deadline = started + wall_timeout;
    let mut cpu_poll = tokio::time::interval_at(started + CPU_POLL_INTERVAL, CPU_POLL_INTERVAL);
    cpu_poll.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);

    let startup = startup_status.wait_for_startup(EXEC_STARTUP_TIMEOUT).await;
    let end = if !matches!(startup, Ok(ExecStartupOutcome::TargetExecSucceeded)) {
        health.latch_enforcement_failure();
        CommandEnd::Enforcement
    } else {
        let mut child_wait = Box::pin(child.wait());
        loop {
            tokio::select! {
                biased;
                status = &mut child_wait => break CommandEnd::Exited(status),
                changed = cancel.changed() => {
                    if changed.is_err() || *cancel.borrow() {
                        break CommandEnd::Canceled;
                    }
                }
                _ = tokio::time::sleep_until(wall_deadline) => break CommandEnd::WallTimeout,
                _ = cpu_poll.tick(), if cpu_budget.is_some() => {
                    let budget = cpu_budget.expect("guarded by is_some");
                    if cpu_read_failure {
                        break CommandEnd::CpuRead("injected CPU usage read failure".to_string());
                    }
                    let usage = match cgroup.as_ref() {
                        Some(group) => match group.cpu_usage() {
                            Ok(usage) => usage,
                            Err(error) => break CommandEnd::CpuRead(error.to_string()),
                        },
                        #[cfg(any(test, feature = "c7-test-support"))]
                        None => started.elapsed(),
                        #[cfg(all(not(test), not(feature = "c7-test-support")))]
                        None => break CommandEnd::CpuRead("CPU budget requires a cgroup".to_string()),
                    };
                    if usage >= budget {
                        break CommandEnd::CpuBudget;
                    }
                }
            }
        }
    };

    if matches!(end, CommandEnd::CpuRead(_)) {
        health.latch_enforcement_failure();
    }
    let slow_owner_health = health.clone();
    let lifecycle_receipt = lifecycle
        .revoke_and_wait_with_slow_observer(move || {
            slow_owner_health.latch_cleanup_failure();
        })
        .await;
    let receipt = match lifecycle_receipt {
        Ok(receipt) => receipt,
        Err(error) => {
            deferred.retain(DeferredCommandCleanup::new_running(
                child,
                child_id,
                cgroup,
                stdout_task,
                stderr_task,
                cleanup_dir,
                cleanup_probe,
            ));
            health.latch_cleanup_failure();
            return Err(SupervisorError::Cleanup(format!(
                "command binding cleanup failed: {error}"
            )));
        }
    };
    tracing::info!(
        command_cgroup = %cgroup_name,
        control_reader_outcome = receipt.control_reader.marker_label(),
        spawned_sessions = receipt.guardian.spawned_sessions,
        joined_sessions = receipt.guardian.joined_sessions,
        joinset_empty = receipt.guardian.joinset_empty,
        job_channel_closed = receipt.guardian.job_channel_closed,
        "command_binding_owned_drain_complete"
    );
    record_supervision_marker(&supervision_trace, "command_binding_owned_drain_complete");
    if let Some(probe) = &cleanup_probe {
        probe.record_process_group();
    }
    let process_group_cleanup = cleanup_command_process_group(child_id);
    let terminal_error = match &end {
        CommandEnd::WallTimeout => Some(SupervisorError::TimedOut),
        CommandEnd::Canceled => Some(SupervisorError::Canceled),
        CommandEnd::CpuBudget => Some(SupervisorError::CpuBudgetExceeded),
        CommandEnd::CpuRead(error) => Some(SupervisorError::Command(format!(
            "read command CPU usage failed: {error}"
        ))),
        CommandEnd::Enforcement => Some(SupervisorError::Unavailable(health.reason())),
        CommandEnd::Exited(_) => None,
    };
    let status = match end {
        CommandEnd::Exited(status) => status,
        _ => {
            if let Err(error) = child.start_kill()
                && error.kind() != io::ErrorKind::InvalidInput
            {
                tracing::warn!(%error, "failed to kill supervised command");
            }
            child.wait().await
        }
    };
    let had_cgroup = cgroup.is_some();
    if let Some(probe) = &cleanup_probe {
        probe.record_cgroup();
    }
    let cgroup_cleanup = if cgroup_cleanup_failure {
        Err("injected command cgroup cleanup failure".to_string())
    } else {
        match cgroup.take() {
            Some(group) => group
                .kill_wait_remove()
                .await
                .map_err(|error| error.to_string()),
            None => Ok(()),
        }
    };
    if cgroup_cleanup.is_ok() && (had_cgroup || supervision_trace.is_some()) {
        tracing::info!(
            command_cgroup = %cgroup_name,
            "command_cgroup_cleanup_complete"
        );
        record_supervision_marker(&supervision_trace, "command_cgroup_cleanup_complete");
    }
    let stdout = join_collector(stdout_task, "stdout").await;
    let stderr = join_collector(stderr_task, "stderr").await;
    if let Some(probe) = &cleanup_probe {
        probe.record_jail();
    }
    let jail_cleanup = cleanup_directory(cleanup_dir.as_deref()).await;

    let mut cleanup_errors = Vec::new();
    if let Err(error) = process_group_cleanup {
        cleanup_errors.push(format!("command process group cleanup failed: {error}"));
    }
    if let Err(error) = cgroup_cleanup {
        cleanup_errors.push(format!("command cgroup cleanup failed: {error}"));
    }
    if let Err(error) = jail_cleanup {
        cleanup_errors.push(format!("command jail cleanup failed: {error}"));
    }
    if let Err(error) = &stdout {
        cleanup_errors.push(format!("command stdout drain failed: {error}"));
    }
    if let Err(error) = &stderr {
        cleanup_errors.push(format!("command stderr drain failed: {error}"));
    }
    if !cleanup_errors.is_empty() {
        health.latch_cleanup_failure();
        return Err(SupervisorError::Cleanup(cleanup_errors.join("; ")));
    }
    if let Some(error) = terminal_error {
        return Err(error);
    }
    let status = status.map_err(|error| {
        SupervisorError::Command(format!("waiting for command failed: {error}"))
    })?;
    let stdout = stdout?;
    let stderr = stderr?;
    Ok(CommandOutput {
        exit_code: status.code().unwrap_or(-1),
        truncated: stdout.truncated || stderr.truncated,
        stdout: stdout.text,
        stderr: stderr.text,
        cgroup_name,
    })
}

fn record_supervision_marker(trace: &Option<Arc<Mutex<Vec<&'static str>>>>, marker: &'static str) {
    if let Some(trace) = trace
        && let Ok(mut trace) = trace.lock()
    {
        trace.push(marker);
    }
}
