#[cfg(target_os = "linux")]
mod real_cgroup {
    use agent_runtime::{
        cgroup::{CgroupConfig, CommandIdentity, CommandLimits},
        exec::{CommandSupervisor, ExecTarget},
    };
    use std::{env, path::PathBuf, time::Duration};

    #[tokio::test]
    #[ignore = "requires an empty delegated cgroup v2 subtree owned by the test UID"]
    async fn first_untrusted_instruction_is_already_in_command_cgroup() {
        let root = PathBuf::from(
            env::var_os("AGENT_RUNTIME_TEST_CGROUP_ROOT")
                .expect("AGENT_RUNTIME_TEST_CGROUP_ROOT must name a delegated cgroup v2 subtree"),
        );
        let helper = PathBuf::from(env!("CARGO_BIN_EXE_agent-runtime-exec"));
        let timeout = Duration::from_secs(5);
        let limits = CommandLimits::approved_defaults().with_cpu_budget(timeout);
        let supervisor = CommandSupervisor::production(
            CgroupConfig {
                root: root.clone(),
                limits,
            },
            helper,
        )
        .unwrap();
        supervisor.recover_stale().await.unwrap();
        let identity = CommandIdentity::new("probe", "first-instruction", "membership");
        let cgroup_name = identity.cgroup_name();
        let target = ExecTarget {
            program: PathBuf::from("bash"),
            args: vec![
                "-lc".to_string(),
                "cat /proc/self/cgroup; cat /sys/fs/cgroup$(awk -F: '$1==\"0\" {print $3}' /proc/self/cgroup)/{pids.max,memory.max,memory.swap.max,memory.oom.group,cpu.max}"
                    .to_string(),
            ],
            cwd: env::current_dir().unwrap(),
        };

        let output = supervisor
            .start_command(
                identity,
                target,
                vec![
                    ("PATH".to_string(), "/usr/bin:/bin".to_string()),
                    ("HOME".to_string(), "/tmp".to_string()),
                ],
                timeout,
                12_000,
                None,
            )
            .unwrap()
            .wait()
            .await
            .unwrap();

        assert!(output.stdout.contains(&cgroup_name), "{}", output.stdout);
        for expected in ["64", "268435456", "0", "1", "100000 100000"] {
            assert!(output.stdout.contains(expected), "{}", output.stdout);
        }
        assert_eq!(output.cgroup_name, cgroup_name);
        assert!(!root.join(cgroup_name).exists());
    }

    #[tokio::test]
    #[ignore = "requires an empty delegated cgroup v2 subtree owned by the test UID"]
    async fn cpu_budget_kills_and_removes_the_entire_command_cgroup() {
        let root = PathBuf::from(
            env::var_os("AGENT_RUNTIME_TEST_CGROUP_ROOT")
                .expect("AGENT_RUNTIME_TEST_CGROUP_ROOT must name a delegated cgroup v2 subtree"),
        );
        let helper = PathBuf::from(env!("CARGO_BIN_EXE_agent-runtime-exec"));
        let mut limits = CommandLimits::approved_defaults();
        limits.cpu_budget = Duration::from_millis(100);
        let supervisor = CommandSupervisor::production(
            CgroupConfig {
                root: root.clone(),
                limits,
            },
            helper,
        )
        .unwrap();
        let identity = CommandIdentity::new("probe", "cpu-budget", "busy-loop");
        let cgroup_name = identity.cgroup_name();
        let target = ExecTarget {
            program: PathBuf::from("bash"),
            args: vec!["-lc".to_string(), "while :; do :; done".to_string()],
            cwd: env::current_dir().unwrap(),
        };

        let error = supervisor
            .start_command(
                identity,
                target,
                vec![("PATH".to_string(), "/usr/bin:/bin".to_string())],
                Duration::from_secs(5),
                12_000,
                None,
            )
            .unwrap()
            .wait()
            .await
            .unwrap_err();

        assert!(error.to_string().contains("CPU budget"));
        assert!(!root.join(cgroup_name).exists());
    }
}

#[test]
fn unified_cgroup_parser_accepts_exactly_one_absolute_v2_entry() {
    use agent_runtime::cgroup::parse_unified_cgroup_path;
    assert_eq!(
        parse_unified_cgroup_path("0::/runtime.slice/service\n").unwrap(),
        std::path::PathBuf::from("/runtime.slice/service")
    );
    for invalid in [
        "",
        "1:name=/legacy\n",
        "0::relative\n",
        "0::/ok\n0::/duplicate\n",
        "0::/runtime/../escape\n",
        "0::/runtime\n1:name=/legacy\n",
    ] {
        assert!(parse_unified_cgroup_path(invalid).is_err(), "{invalid:?}");
    }
}

#[test]
fn topology_requires_exact_direct_siblings_controllers_and_aggregate_limits() {
    use agent_runtime::cgroup::CgroupTopologyConfig;
    use std::fs;

    let fixture = tempfile::tempdir().unwrap();
    let mount = fixture.path().join("mount");
    let aggregate = mount.join("aggregate");
    let service = aggregate.join("service");
    let commands = aggregate.join("commands");
    fs::create_dir_all(&service).unwrap();
    fs::create_dir(&commands).unwrap();
    fs::write(aggregate.join("cgroup.controllers"), "pids memory cpu").unwrap();
    fs::write(aggregate.join("cgroup.subtree_control"), "pids memory cpu").unwrap();
    fs::write(aggregate.join("pids.max"), "512\n").unwrap();
    fs::write(aggregate.join("memory.max"), "1073741824\n").unwrap();
    fs::write(aggregate.join("memory.swap.max"), "0\n").unwrap();
    fs::write(aggregate.join("cpu.max"), "200000 100000\n").unwrap();
    let topology = CgroupTopologyConfig {
        aggregate_root: aggregate.clone(),
        commands_root: commands.clone(),
    };
    topology
        .validate_runtime_from("0::/aggregate/service\n", &mount)
        .unwrap();

    let nested = commands.join("nested");
    fs::create_dir(&nested).unwrap();
    let nested_topology = CgroupTopologyConfig {
        aggregate_root: aggregate.clone(),
        commands_root: nested,
    };
    assert!(
        nested_topology
            .validate_runtime_from("0::/aggregate/service\n", &mount)
            .is_err()
    );

    fs::write(aggregate.join("cpu.max"), "100000 100000\n").unwrap();
    assert!(
        topology
            .validate_runtime_from("0::/aggregate/service\n", &mount)
            .unwrap_err()
            .to_string()
            .contains("200000 100000")
    );
}

#[cfg(all(target_os = "linux", feature = "c7-test-support"))]
#[test]
fn c7_cgroup_create_failure_latches_and_cancels_active() {
    let receipt = agent_runtime::c7_test_support::create_failure();
    assert!(receipt.operation_failed);
    assert!(receipt.active_cancelled);
    assert!(!receipt.health.is_ready());
    assert_eq!(
        receipt.health.reason(),
        "bash unavailable: command enforcement failed"
    );
}

#[cfg(all(target_os = "linux", feature = "c7-test-support"))]
#[test]
fn c7_each_limit_control_write_failure_latches() {
    for control in agent_runtime::c7_test_support::LIMIT_CONTROLS {
        let receipt = agent_runtime::c7_test_support::limit_control_failure(control);
        assert!(receipt.operation_failed, "{control}");
        assert!(!receipt.health.is_ready(), "{control}");
        assert_eq!(
            receipt.health.reason(),
            "bash unavailable: command enforcement failed",
            "{control}"
        );
    }
}

#[cfg(all(target_os = "linux", feature = "c7-test-support"))]
#[test]
fn c7_cpu_usage_read_and_parse_failure_latch() {
    for parse_failure in [false, true] {
        let receipt = agent_runtime::c7_test_support::cpu_usage_failure(parse_failure);
        assert!(receipt.operation_failed);
        assert!(!receipt.health.is_ready());
        assert_eq!(
            receipt.health.reason(),
            "bash unavailable: command enforcement failed"
        );
    }
}

#[cfg(all(target_os = "linux", feature = "c7-test-support"))]
#[tokio::test]
async fn c7_enforcement_latch_is_irreversible_but_local_apis_remain() {
    use agent_runtime::{
        AppState, BashSandboxMode, app, namespace_gate::NamespaceGate,
        runtime_fetch_proxy::RuntimeFetchProxy, skills::FrozenSkillSnapshot, trace::JsonlTraceSink,
        workspace_budget::WorkspaceBudget,
    };
    use axum::{body::Body, http::Request};
    use std::time::Duration;
    use tower::ServiceExt as _;

    let health = agent_runtime::c7_test_support::irreversible_health();
    assert!(!health.is_ready());
    assert_eq!(
        health.reason(),
        "bash unavailable: command enforcement failed"
    );
    let root = tempfile::tempdir().unwrap();
    let skills = tempfile::tempdir().unwrap();
    let budget = WorkspaceBudget::new(root.path(), 1024 * 1024).unwrap();
    let state = AppState {
        workspace_root: root.path().to_path_buf(),
        skills_root: Some(skills.path().to_path_buf()),
        skill_snapshot: FrozenSkillSnapshot::empty().unwrap(),
        auth_token: None,
        max_output_chars: 1024,
        command_timeout: Duration::from_secs(1),
        trace_sink: JsonlTraceSink::new(root.path().join("trace.jsonl")),
        bash_sandbox: BashSandboxMode::Proot,
        command_supervisor: None,
        bash_health: health.clone(),
        fetch_proxy: RuntimeFetchProxy::disabled(budget.clone()),
        require_fetch_for_readiness: false,
        bash_readiness_error: health.reason(),
        workspace_budget: budget,
        namespace_gate: NamespaceGate::default(),
    };
    let response = app(state.clone())
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/v1/write")
                .header("content-type", "application/json")
                .body(Body::from(
                    r#"{"namespace":"c7","run_id":"local","path":"note.txt","content":"still available"}"#,
                ))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), 200);
    let response = app(state)
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/v1/read")
                .header("content-type", "application/json")
                .body(Body::from(
                    r#"{"namespace":"c7","run_id":"local","path":"note.txt"}"#,
                ))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), 200);
    assert_eq!(
        health.reason(),
        "bash unavailable: command enforcement failed"
    );
}

#[cfg(target_os = "linux")]
#[test]
#[ignore = "requires the deployed aggregate/service/commands cgroup sibling topology"]
fn deployed_runtime_and_commands_are_validated_as_direct_aggregate_children() {
    use agent_runtime::cgroup::CgroupTopologyConfig;
    use std::{env, path::PathBuf};
    let topology = CgroupTopologyConfig {
        aggregate_root: PathBuf::from(
            env::var_os("AGENT_RUNTIME_TEST_CGROUP_AGGREGATE_ROOT")
                .expect("AGENT_RUNTIME_TEST_CGROUP_AGGREGATE_ROOT is required"),
        ),
        commands_root: PathBuf::from(
            env::var_os("AGENT_RUNTIME_TEST_CGROUP_ROOT")
                .expect("AGENT_RUNTIME_TEST_CGROUP_ROOT is required"),
        ),
    };
    topology.validate_runtime().unwrap();
}
