use super::{FixtureRoot, fixture_cgroups};
use crate::exec::spawn::c7_test_support::DescriptorReleaseProbe;
use crate::{
    cgroup::CommandIdentity,
    exec::{CommandSupervisor, ExecTarget, SpawnControls, SupervisorError},
    runtime_fetch_proxy::{CommandBindingPhase, RuntimeFetchProxy},
    workspace_budget::WorkspaceBudget,
};
use std::{path::PathBuf, time::Duration};

pub use crate::exec::spawn::fd_map::c7_test_support::FD_INSTALL_FAULT_STAGES;
use crate::exec::spawn::fd_map::c7_test_support::FD_INSTALL_FAULTS;

const AUTHORITATIVE_SELECTOR: &str = "c7_each_three_fd_mapping_stage_failure_aborts_and_latches";
const HELPER_MODE_SKIP: &str = "__c7_fd_exec_helper_mode__";

pub struct FdFaultTableReceipt {
    pub success_control_marker: bool,
    pub rows: Vec<FdFaultRowReceipt>,
}

pub struct FdFaultRowReceipt {
    pub stage: &'static str,
    pub current_command_failed: bool,
    pub target_exec_marker: bool,
    pub health_ready: bool,
    pub health_reason_stable: bool,
    pub binding_phase: CommandBindingPhase,
    pub binding_registry_entries: usize,
    pub cgroup_removed: bool,
    pub cgroup_cleanup_count: usize,
    pub deferred_cleanup_count: usize,
    pub local_descriptors_released: bool,
    pub subsequent_bash_rejected: bool,
}

pub fn enter_fd_exec_helper_if_requested() {
    if std::env::args_os().any(|arg| arg == HELPER_MODE_SKIP) {
        match crate::exec::exec_from_config_fd(crate::exec::EXEC_CONFIG_FD) {
            Ok(never) => match never {},
            Err(_) => std::process::exit(78),
        }
    }
}

pub async fn fd_mapping_fault_table() -> FdFaultTableReceipt {
    let success_control_marker = run_success_control().await;
    let mut rows = Vec::with_capacity(FD_INSTALL_FAULTS.len());
    for (fault, stage) in FD_INSTALL_FAULTS.into_iter().zip(FD_INSTALL_FAULT_STAGES) {
        rows.push(run_fault_row(fault, stage).await);
    }
    FdFaultTableReceipt {
        success_control_marker,
        rows,
    }
}

async fn run_success_control() -> bool {
    let root = FixtureRoot::new("fd-success");
    let cgroup_root = setup_cgroup_root(&root);
    let cgroups = fixture_cgroups(&cgroup_root);
    let cleanup = cgroups.clone();
    let descriptor_probe = DescriptorReleaseProbe::default();
    let supervisor = production_supervisor(cgroups, None, descriptor_probe.clone());
    let health = supervisor.health();
    let proxy = runtime_proxy(&root);
    let marker = root.path().join("target-exec-marker");
    let command_id = "fd-success";
    let launch = command_launch(&proxy, &health, command_id);
    let lifecycle = launch.lifecycle.clone();
    let identity = CommandIdentity::new("c7", "fd-success", command_id);
    let cgroup_path = cgroup_root.join(identity.cgroup_name());
    let handle = supervisor
        .start_command_with_launch(
            identity,
            marker_target(&marker),
            move || Ok(launch),
            Duration::from_secs(2),
            1024,
            None,
        )
        .unwrap();
    let output = handle.wait().await.unwrap();
    assert_eq!(output.exit_code, 0);
    assert!(health.is_ready());
    assert_eq!(
        lifecycle.phase().unwrap(),
        Some(CommandBindingPhase::Drained)
    );
    assert_eq!(proxy.active_binding_count().unwrap(), 0);
    assert!(!cgroup_path.exists());
    assert_eq!(cleanup.fixture_kill_log().len(), 1);
    assert_eq!(supervisor.deferred_cleanup_count_for_tests().await, 0);
    assert!(descriptor_probe.all_released());
    marker.exists()
}

async fn run_fault_row(
    fault: crate::exec::spawn::fd_map::FdInstallStage,
    stage: &'static str,
) -> FdFaultRowReceipt {
    let root = FixtureRoot::new(stage);
    let cgroup_root = setup_cgroup_root(&root);
    let cgroups = fixture_cgroups(&cgroup_root);
    let cleanup = cgroups.clone();
    let descriptor_probe = DescriptorReleaseProbe::default();
    let supervisor = production_supervisor(cgroups, Some(fault), descriptor_probe.clone());
    let health = supervisor.health();
    let proxy = runtime_proxy(&root);
    let marker = root.path().join("target-exec-marker");
    let command_id = format!("fd-{stage}");
    let launch = command_launch(&proxy, &health, &command_id);
    let lifecycle = launch.lifecycle.clone();
    let identity = CommandIdentity::new("c7", stage, &command_id);
    let cgroup_path = cgroup_root.join(identity.cgroup_name());
    let current = supervisor.start_command_with_launch(
        identity,
        marker_target(&marker),
        move || Ok(launch),
        Duration::from_secs(2),
        1024,
        None,
    );
    let binding_phase = lifecycle.phase().unwrap().unwrap();
    let binding_registry_entries = proxy.active_binding_count().unwrap();
    let cgroup_cleanup_count = cleanup.fixture_kill_log().len();
    let deferred_cleanup_count = supervisor.deferred_cleanup_count_for_tests().await;
    let reason = health.reason();
    let subsequent = supervisor.start(marker_target(&marker), Vec::new(), Duration::from_secs(1));
    FdFaultRowReceipt {
        stage,
        current_command_failed: matches!(current, Err(SupervisorError::Unavailable(_))),
        target_exec_marker: marker.exists(),
        health_ready: health.is_ready(),
        health_reason_stable: health.reason() == reason,
        binding_phase,
        binding_registry_entries,
        cgroup_removed: !cgroup_path.exists(),
        cgroup_cleanup_count,
        deferred_cleanup_count,
        local_descriptors_released: descriptor_probe.all_released(),
        subsequent_bash_rejected: matches!(subsequent, Err(SupervisorError::Unavailable(_))),
    }
}

fn production_supervisor(
    cgroups: crate::cgroup::CgroupManager,
    fault: Option<crate::exec::spawn::fd_map::FdInstallStage>,
    descriptor_probe: DescriptorReleaseProbe,
) -> CommandSupervisor {
    CommandSupervisor::test_production_with_spawn_controls(
        cgroups,
        std::env::current_exe().unwrap(),
        SpawnControls {
            fd_install_fault: fault,
            helper_args: Some(helper_args()),
            fixture_cpu_stat: true,
            descriptor_probe: Some(descriptor_probe),
            ..SpawnControls::default()
        },
    )
}

fn helper_args() -> Vec<String> {
    vec![
        AUTHORITATIVE_SELECTOR.to_string(),
        "--exact".to_string(),
        "--test-threads=1".to_string(),
        "--skip".to_string(),
        HELPER_MODE_SKIP.to_string(),
    ]
}

fn setup_cgroup_root(root: &FixtureRoot) -> PathBuf {
    let cgroup_root = root.path().join("cgroups");
    std::fs::create_dir(&cgroup_root).unwrap();
    std::fs::write(cgroup_root.join("cgroup.controllers"), "pids memory cpu").unwrap();
    std::fs::write(
        cgroup_root.join("cgroup.subtree_control"),
        "pids memory cpu",
    )
    .unwrap();
    std::fs::write(cgroup_root.join("cgroup.procs"), "").unwrap();
    cgroup_root
}

fn runtime_proxy(root: &FixtureRoot) -> RuntimeFetchProxy {
    let workspace = root.path().join("workspace");
    std::fs::create_dir(&workspace).unwrap();
    RuntimeFetchProxy::disabled(WorkspaceBudget::new(workspace, 1024).unwrap())
}

fn command_launch(
    proxy: &RuntimeFetchProxy,
    health: &crate::exec::BashHealth,
    command_id: &str,
) -> crate::runtime_fetch_proxy::CommandLaunch {
    proxy
        .bind_command(
            "c7",
            "fd-map",
            command_id.to_string(),
            Duration::from_secs(2),
            health.clone(),
        )
        .unwrap()
        .into_launch(proxy.shell_environment())
        .unwrap()
}

fn marker_target(marker: &std::path::Path) -> ExecTarget {
    ExecTarget {
        program: PathBuf::from("/usr/bin/touch"),
        args: vec![marker.display().to_string()],
        cwd: PathBuf::from("/tmp"),
    }
}
