use super::{
    CommandSupervisor, ExecTarget, RlimitSpec, SupervisorError, helper_argv, validate_environment,
};
#[cfg(not(target_os = "linux"))]
use crate::cgroup::{CgroupConfig, CommandLimits};
use crate::runtime_fetch_proxy::{CommandLaunch, CommandLifecycleLease, RuntimeFetchProxy};
use std::{path::PathBuf, time::Duration};

fn shell_target(command: &str) -> ExecTarget {
    if cfg!(windows) {
        ExecTarget {
            program: PathBuf::from("cmd"),
            args: vec!["/C".to_string(), command.to_string()],
            cwd: std::env::current_dir().unwrap(),
        }
    } else {
        ExecTarget {
            program: PathBuf::from("bash"),
            args: vec!["-lc".to_string(), command.to_string()],
            cwd: std::env::current_dir().unwrap(),
        }
    }
}

fn sleep_command() -> &'static str {
    if cfg!(windows) {
        "for /L %i in (1,1,2147483647) do @rem"
    } else {
        "sleep 2"
    }
}

#[test]
fn approved_rlimits_are_exact() {
    let limits = RlimitSpec::approved_defaults();
    assert_eq!(limits.nproc, 480);
    assert_eq!(limits.nofile, 256);
    assert_eq!(limits.fsize_bytes, 64 * 1024 * 1024);
    assert_eq!(limits.core_bytes, 0);
}

#[test]
fn helper_argv_contains_only_the_bounded_config_descriptor() {
    let args = helper_argv();
    assert_eq!(args, ["--config-fd", "3"]);
    assert!(!args.iter().any(|arg| arg.contains("secret")));
}

#[test]
fn helper_environment_is_allowlisted() {
    validate_environment(&[
        ("PATH".to_string(), "/bin".to_string()),
        ("HOME".to_string(), "/tmp".to_string()),
    ])
    .unwrap();
    let error =
        validate_environment(&[("LD_PRELOAD".to_string(), "/workspace/escape.so".to_string())])
            .unwrap_err();
    assert!(error.to_string().contains("LD_PRELOAD"));
}

#[tokio::test]
async fn direct_supervisor_reports_normal_exit() {
    let output = CommandSupervisor::test_direct()
        .start(
            shell_target("echo supervised"),
            Vec::new(),
            Duration::from_secs(2),
        )
        .unwrap()
        .wait()
        .await
        .unwrap();
    assert_eq!(output.exit_code, 0);
    assert!(output.stdout.contains("supervised"));
}

#[tokio::test]
async fn direct_supervisor_terminates_wall_timeout() {
    let error = CommandSupervisor::test_direct()
        .start(
            shell_target(sleep_command()),
            Vec::new(),
            Duration::from_millis(25),
        )
        .unwrap()
        .wait()
        .await
        .unwrap_err();
    assert!(matches!(error, SupervisorError::TimedOut));
}

#[tokio::test]
async fn direct_supervisor_terminates_cancellation() {
    let handle = CommandSupervisor::test_direct()
        .start(
            shell_target(sleep_command()),
            Vec::new(),
            Duration::from_secs(5),
        )
        .unwrap();
    let error = handle.cancel().await.unwrap_err();
    assert!(matches!(error, SupervisorError::Canceled));
}

#[tokio::test]
async fn graceful_shutdown_latches_health_before_cancel_and_drains_active_commands() {
    let supervisor = CommandSupervisor::test_direct();
    let health = supervisor.health();
    let handle = supervisor
        .start(
            shell_target(sleep_command()),
            Vec::new(),
            Duration::from_secs(5),
        )
        .unwrap();
    assert_eq!(health.active_command_count(), 1);

    let shutdown = tokio::spawn({
        let supervisor = supervisor.clone();
        async move { supervisor.shutdown().await }
    });
    tokio::task::yield_now().await;

    assert!(!health.is_ready());
    assert_eq!(
        health.reason(),
        "bash unavailable: runtime is shutting down"
    );
    assert!(matches!(
        supervisor.start(
            shell_target("echo must-not-start"),
            Vec::new(),
            Duration::from_secs(1),
        ),
        Err(SupervisorError::Unavailable(_))
    ));
    assert!(matches!(
        handle.wait().await,
        Err(SupervisorError::Canceled)
    ));
    shutdown.await.unwrap().unwrap();
    assert_eq!(health.active_command_count(), 0);
}

#[tokio::test]
async fn c7_deferred_cgroup_cleanup_waits_for_shutdown_drain_receipt() {
    let (supervisor, events, cleanup) = CommandSupervisor::test_direct_with_cleanup_probe();
    let health = supervisor.health();
    let cleanup_dir = tempfile::tempdir().unwrap().keep();
    std::fs::write(cleanup_dir.join("still-owned"), b"fixture").unwrap();
    let (release_tx, release_rx) = tokio::sync::oneshot::channel();
    let (proxy, lifecycle) = RuntimeFetchProxy::with_test_binding_for_tests(
        health.clone(),
        async { Ok(()) },
        async move {
            release_rx.await.unwrap();
            Ok((0, 0))
        },
    );
    let handle = supervisor
        .start_command_with_launch(
            crate::cgroup::CommandIdentity::new("slow", "owner", "cleanup"),
            shell_target("echo complete-after-drain"),
            move || {
                CommandLaunch::with_lifecycle_for_tests(Vec::new(), lifecycle)
                    .map_err(|error| error.to_string())
            },
            Duration::from_secs(5),
            1024,
            Some(cleanup_dir.clone()),
        )
        .unwrap();
    let error = tokio::time::timeout(Duration::from_millis(1_250), handle.wait())
        .await
        .expect("command request must return after bounded binding drain")
        .unwrap_err();

    assert!(matches!(error, SupervisorError::Cleanup(_)));
    assert_eq!(health.reason(), "bash unavailable: command cleanup failed");
    assert!(cleanup_dir.exists());
    assert_eq!(supervisor.deferred_cleanup_count_for_tests().await, 1);
    assert_eq!(cleanup.process_group_calls(), 0);
    assert_eq!(cleanup.cgroup_calls(), 0);
    assert_eq!(cleanup.jail_calls(), 0);
    assert!(events.lock().unwrap().is_empty());
    assert_eq!(proxy.active_binding_count().unwrap(), 1);
    assert!(matches!(
        supervisor.start(
            shell_target("echo must-not-start"),
            Vec::new(),
            Duration::from_secs(1),
        ),
        Err(SupervisorError::Unavailable(_))
    ));
    release_tx.send(()).unwrap();
    proxy.shutdown().await.unwrap();
    assert_eq!(proxy.active_binding_count().unwrap(), 0);
    supervisor.recover_stale().await.unwrap();

    assert!(!cleanup_dir.exists());
    assert_eq!(supervisor.deferred_cleanup_count_for_tests().await, 0);
    assert_eq!(cleanup.stale_recovery_calls(), 1);
    assert_eq!(cleanup.process_group_calls(), 1);
    assert_eq!(cleanup.cgroup_calls(), 1);
    assert_eq!(cleanup.jail_calls(), 1);
    assert!(events.lock().unwrap().is_empty());
    assert_eq!(health.active_command_count(), 0);
}

#[tokio::test]
async fn c7_guardian_receipt_mismatch_blocks_cgroup_cleanup() {
    let (supervisor, events, cleanup) = CommandSupervisor::test_direct_with_cleanup_probe();
    let health = supervisor.health();
    let cleanup_dir = tempfile::tempdir().unwrap().keep();
    std::fs::write(cleanup_dir.join("must-be-removed"), b"fixture").unwrap();
    let lifecycle = CommandLifecycleLease::with_test_tasks(async { Ok(()) }, async { Ok((2, 1)) });
    let handle = supervisor
        .start_command_with_launch(
            crate::cgroup::CommandIdentity::new("failed", "owner", "cleanup"),
            shell_target("echo owner-fails"),
            move || {
                CommandLaunch::with_lifecycle_for_tests(Vec::new(), lifecycle)
                    .map_err(|error| error.to_string())
            },
            Duration::from_secs(5),
            1024,
            Some(cleanup_dir.clone()),
        )
        .unwrap();

    let error = handle.wait().await.unwrap_err();

    assert!(matches!(error, SupervisorError::Cleanup(_)));
    assert!(error.to_string().contains("command binding cleanup failed"));
    assert!(cleanup_dir.exists());
    assert!(!health.is_ready());
    assert_eq!(supervisor.deferred_cleanup_count_for_tests().await, 1);
    assert_eq!(cleanup.process_group_calls(), 0);
    assert_eq!(cleanup.cgroup_calls(), 0);
    assert_eq!(cleanup.jail_calls(), 0);
    assert!(events.lock().unwrap().is_empty());
    assert_eq!(health.active_command_count(), 0);
}

#[tokio::test]
async fn direct_supervisor_exercises_cpu_budget_cleanup_branch() {
    let supervisor =
        CommandSupervisor::test_direct_with_cpu_budget(Some(Duration::from_millis(20)));
    let error = supervisor
        .start(
            shell_target(sleep_command()),
            Vec::new(),
            Duration::from_secs(5),
        )
        .unwrap()
        .wait()
        .await
        .unwrap_err();
    assert!(matches!(error, SupervisorError::CpuBudgetExceeded));
}

#[tokio::test]
async fn enforcement_failures_latch_one_health_fuse_but_target_exit_one_does_not() {
    for stage in super::ExecInitFailureStage::ALL {
        let supervisor = CommandSupervisor::test_direct_with_exec_failure_stage(stage);
        let health = supervisor.health();
        let error = supervisor
            .start(shell_target("exit 0"), Vec::new(), Duration::from_secs(2))
            .unwrap()
            .wait()
            .await
            .unwrap_err();
        assert!(
            matches!(error, SupervisorError::Unavailable(_)),
            "{stage:?}"
        );
        assert!(!health.is_ready(), "{stage:?}");
        assert_eq!(
            health.reason(),
            "bash unavailable: command enforcement failed"
        );
        assert!(matches!(
            supervisor.start(shell_target("exit 0"), Vec::new(), Duration::from_secs(1),),
            Err(SupervisorError::Unavailable(_))
        ));
    }

    let supervisor = CommandSupervisor::test_direct();
    let output = supervisor
        .start(shell_target("exit 1"), Vec::new(), Duration::from_secs(2))
        .unwrap()
        .wait()
        .await
        .unwrap();
    assert_eq!(output.exit_code, 1);
    assert!(supervisor.health().is_ready());
}

#[tokio::test]
async fn malformed_multiple_and_timed_out_helper_status_latch_health() {
    let malformed = vec![0xa7];
    let truncated =
        super::encode_exec_status_failure(super::ExecInitFailureStage::ConfigRead)[..3].to_vec();
    let multiple = [
        super::encode_exec_status_failure(super::ExecInitFailureStage::ConfigRead).as_slice(),
        super::encode_exec_status_failure(super::ExecInitFailureStage::TargetExec).as_slice(),
    ]
    .concat();
    for payload in [malformed, truncated, multiple] {
        let supervisor = CommandSupervisor::test_direct_with_exec_status_payload(payload);
        let error = supervisor
            .start(shell_target("exit 0"), Vec::new(), Duration::from_secs(2))
            .unwrap()
            .wait()
            .await
            .unwrap_err();
        assert!(matches!(error, SupervisorError::Unavailable(_)));
        assert!(!supervisor.health().is_ready());
    }

    let supervisor = CommandSupervisor::test_direct_with_exec_status_timeout();
    let error = supervisor
        .start(shell_target("exit 0"), Vec::new(), Duration::from_secs(2))
        .unwrap()
        .wait()
        .await
        .unwrap_err();
    assert!(matches!(error, SupervisorError::Unavailable(_)));
    assert!(!supervisor.health().is_ready());

    let supervisor = CommandSupervisor::test_direct_with_exec_status_read_error();
    let error = supervisor
        .start(shell_target("exit 0"), Vec::new(), Duration::from_secs(2))
        .unwrap()
        .wait()
        .await
        .unwrap_err();
    assert!(matches!(error, SupervisorError::Unavailable(_)));
    assert!(!supervisor.health().is_ready());
}

#[tokio::test]
async fn post_start_cgroup_create_and_cpu_read_failures_latch_health() {
    let create_failure = CommandSupervisor::test_direct_with_cgroup_create_failure();
    let error =
        match create_failure.start(shell_target("exit 0"), Vec::new(), Duration::from_secs(1)) {
            Ok(_) => panic!("injected cgroup creation failure unexpectedly started"),
            Err(error) => error,
        };
    assert!(matches!(error, SupervisorError::Unavailable(_)));
    assert!(!create_failure.health().is_ready());

    let cpu_failure = CommandSupervisor::test_direct_with_cpu_read_failure();
    let error = cpu_failure
        .start(
            shell_target(sleep_command()),
            Vec::new(),
            Duration::from_secs(2),
        )
        .unwrap()
        .wait()
        .await
        .unwrap_err();
    assert!(error.to_string().contains("CPU usage"));
    assert!(!cpu_failure.health().is_ready());
    assert!(matches!(
        cpu_failure.start(shell_target("exit 0"), Vec::new(), Duration::from_secs(1),),
        Err(SupervisorError::Unavailable(_))
    ));
}

#[tokio::test]
async fn trace_markers_are_ordered_and_cleanup_failure_omits_second_marker() {
    let (supervisor, events) = CommandSupervisor::test_direct_with_trace(false);
    supervisor
        .start(shell_target("exit 0"), Vec::new(), Duration::from_secs(2))
        .unwrap()
        .wait()
        .await
        .unwrap();
    assert_eq!(
        events.lock().unwrap().as_slice(),
        [
            "command_binding_owned_drain_complete",
            "command_cgroup_cleanup_complete"
        ]
    );

    let (supervisor, events) = CommandSupervisor::test_direct_with_trace(true);
    let health = supervisor.health();
    let error = supervisor
        .start(shell_target("exit 0"), Vec::new(), Duration::from_secs(2))
        .unwrap()
        .wait()
        .await
        .unwrap_err();
    assert!(matches!(error, SupervisorError::Cleanup(_)));
    assert_eq!(
        events.lock().unwrap().as_slice(),
        ["command_binding_owned_drain_complete"]
    );
    assert!(!health.is_ready());
}

#[tokio::test]
async fn dropping_handle_cancels_owner_and_preserves_cleanup_ownership() {
    let cleanup_dir = tempfile::tempdir().unwrap().keep();
    std::fs::write(cleanup_dir.join("owned-by-supervisor"), b"fixture").unwrap();
    let handle = CommandSupervisor::test_direct()
        .start_command(
            crate::cgroup::CommandIdentity::new("drop", "owner", "cleanup"),
            shell_target(sleep_command()),
            Vec::new(),
            Duration::from_secs(5),
            1024,
            Some(cleanup_dir.clone()),
        )
        .unwrap();

    drop(handle);

    for _ in 0..100 {
        if !cleanup_dir.exists() {
            return;
        }
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    panic!(
        "supervisor did not remove {} after handle cancellation",
        cleanup_dir.display()
    );
}

#[tokio::test]
async fn c7_helper_init_stage_failures_emit_one_exact_record_and_latch() {
    assert_eq!(super::EXEC_STATUS_RECORD_BYTES, 4);
    for (index, stage) in super::ExecInitStage::ALL.into_iter().enumerate() {
        #[cfg(target_os = "linux")]
        let encoded = super::helper::test_support::injected_failure_record(stage);
        #[cfg(not(target_os = "linux"))]
        let encoded = super::ExecStatusRecord { stage }.encode();
        assert_eq!(encoded, [1, 1, u8::try_from(index + 1).unwrap(), 0]);
        let supervisor = CommandSupervisor::test_direct_with_exec_failure_stage(stage);
        let health = supervisor.health();
        let error = supervisor
            .start(shell_target("exit 0"), Vec::new(), Duration::from_secs(1))
            .unwrap()
            .wait()
            .await
            .unwrap_err();
        assert!(matches!(error, SupervisorError::Unavailable(_)));
        assert!(!health.is_ready());
    }
}

#[tokio::test(flavor = "multi_thread")]
async fn c7_spawn_preexec_and_config_writer_failures_latch() {
    for fault in super::HelperLaunchFault::ALL {
        let supervisor = CommandSupervisor::test_direct_with_helper_launch_failure(fault);
        let health = supervisor.health();
        let error =
            match supervisor.start(shell_target("exit 0"), Vec::new(), Duration::from_secs(1)) {
                Ok(_) => panic!("{fault:?} unexpectedly started"),
                Err(error) => error,
            };
        assert!(
            matches!(error, SupervisorError::Unavailable(_)),
            "{fault:?}"
        );
        assert!(!health.is_ready(), "{fault:?}");
        assert!(matches!(
            supervisor.start(shell_target("exit 0"), Vec::new(), Duration::from_secs(1)),
            Err(SupervisorError::Unavailable(_))
        ));
    }
}

#[tokio::test]
async fn c7_helper_status_clean_eof_accepts_target_exit_one_without_latch() {
    assert_eq!(super::EXEC_STARTUP_TIMEOUT, Duration::from_secs(2));
    let outcome = super::read_exec_startup_for_test(Vec::new(), Duration::from_millis(20))
        .await
        .unwrap();
    assert_eq!(outcome, super::ExecStartupOutcome::TargetExecSucceeded);

    let supervisor = CommandSupervisor::test_direct();
    let output = supervisor
        .start(shell_target("exit 1"), Vec::new(), Duration::from_secs(1))
        .unwrap()
        .wait()
        .await
        .unwrap();
    assert_eq!(output.exit_code, 1);
    assert!(supervisor.health().is_ready());
}

#[tokio::test]
async fn c7_helper_status_timeout_malformed_and_read_failure_latch() {
    assert_eq!(super::EXEC_STARTUP_TIMEOUT, Duration::from_secs(2));
    for payload in [
        vec![1],
        vec![1, 1, 1],
        vec![1, 1, 1, 0, 0],
        vec![2, 1, 1, 0],
        vec![1, 2, 1, 0],
        vec![1, 1, 11, 0],
        vec![1, 1, 1, 1],
    ] {
        let supervisor = CommandSupervisor::test_direct_with_exec_status_payload(payload);
        let error = supervisor
            .start(shell_target("exit 0"), Vec::new(), Duration::from_secs(1))
            .unwrap()
            .wait()
            .await
            .unwrap_err();
        assert!(matches!(error, SupervisorError::Unavailable(_)));
        assert!(!supervisor.health().is_ready());
    }
    for supervisor in [
        CommandSupervisor::test_direct_with_exec_status_timeout(),
        CommandSupervisor::test_direct_with_exec_status_read_error(),
    ] {
        let error = supervisor
            .start(shell_target("exit 0"), Vec::new(), Duration::from_secs(1))
            .unwrap()
            .wait()
            .await
            .unwrap_err();
        assert!(matches!(error, SupervisorError::Unavailable(_)));
        assert!(!supervisor.health().is_ready());
    }
}

#[cfg(not(target_os = "linux"))]
#[test]
fn non_linux_production_constructor_is_rejected() {
    let error = CommandSupervisor::production(
        CgroupConfig {
            root: PathBuf::from("unused"),
            limits: CommandLimits::approved_defaults(),
        },
        PathBuf::from("unused-helper"),
    )
    .err()
    .expect("production must fail closed off Linux");

    assert_eq!(
        error.to_string(),
        "agent runtime production execution requires Linux"
    );
}
