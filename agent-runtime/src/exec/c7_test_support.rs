use super::spawn::c7_test_support::DescriptorReleaseProbe;
use super::{CommandSupervisor, ExecTarget, SpawnControls, SupervisorError};
use crate::{
    cgroup::{CgroupManager, CommandIdentity, CommandLimits},
    runtime_fetch_proxy::{CommandLaunch, RuntimeFetchProxy},
    workspace_budget::WorkspaceBudget,
};
use std::{
    path::{Path, PathBuf},
    sync::{
        Arc,
        atomic::{AtomicBool, AtomicU64, Ordering},
    },
    time::Duration,
};

static SEQUENCE: AtomicU64 = AtomicU64::new(0);

mod fd_faults;
pub use fd_faults::{
    FD_INSTALL_FAULT_STAGES, FdFaultRowReceipt, FdFaultTableReceipt,
    enter_fd_exec_helper_if_requested, fd_mapping_fault_table,
};

pub struct DeferredCleanupReceipt {
    pub request_bounded: bool,
    pub cleanup_blocked_before_receipt: bool,
    pub cleanup_completed_after_shutdown: bool,
    pub markers_empty: bool,
}

pub struct TraceReceipt {
    pub events: Vec<&'static str>,
    pub health_ready: bool,
    pub cleanup_failed: bool,
}

pub struct ConfigWriterFailureReceipt {
    pub current_command_failed: bool,
    pub helper_exec_marker: bool,
    pub health_ready: bool,
    pub binding_drained: bool,
    pub binding_registry_entries: usize,
    pub cgroup_removed: bool,
    pub cgroup_cleanup_count: usize,
    pub deferred_cleanup_count: usize,
    pub local_descriptors_released: bool,
    pub subsequent_bash_rejected: bool,
}

pub async fn config_writer_thread_failure() -> ConfigWriterFailureReceipt {
    let root = FixtureRoot::new("writer-thread");
    let cgroup_root = root.path().join("cgroups");
    std::fs::create_dir(&cgroup_root).unwrap();
    std::fs::write(cgroup_root.join("cgroup.controllers"), "pids memory cpu").unwrap();
    std::fs::write(
        cgroup_root.join("cgroup.subtree_control"),
        "pids memory cpu",
    )
    .unwrap();
    std::fs::write(cgroup_root.join("cgroup.procs"), "").unwrap();
    let cgroups = fixture_cgroups(&cgroup_root);
    let cleanup_observer = cgroups.clone();
    let helper_spawn_marker = Arc::new(AtomicBool::new(false));
    let descriptor_probe = DescriptorReleaseProbe::default();
    let supervisor = CommandSupervisor::test_production_with_spawn_controls(
        cgroups,
        std::env::current_exe().unwrap(),
        SpawnControls {
            fail_writer_creation: true,
            helper_spawn_marker: Some(Arc::clone(&helper_spawn_marker)),
            descriptor_probe: Some(descriptor_probe.clone()),
            ..SpawnControls::default()
        },
    );
    let health = supervisor.health();
    let workspace = root.path().join("workspace");
    std::fs::create_dir(&workspace).unwrap();
    let proxy = RuntimeFetchProxy::disabled(WorkspaceBudget::new(&workspace, 1024).unwrap());
    let command_id = "c7-writer-thread";
    let launch = proxy
        .bind_command(
            "c7",
            "writer-thread",
            command_id.to_string(),
            Duration::from_secs(1),
            health.clone(),
        )
        .unwrap()
        .into_launch(proxy.shell_environment())
        .unwrap();
    let identity = CommandIdentity::new("c7", "writer-thread", command_id);
    let cgroup_path = cgroup_root.join(identity.cgroup_name());
    let current = supervisor.start_command_with_launch(
        identity,
        self_target(),
        move || Ok(launch),
        Duration::from_secs(1),
        1024,
        None,
    );
    let binding_registry_entries = proxy.active_binding_count().unwrap();
    let subsequent = supervisor.start(self_target(), Vec::new(), Duration::from_secs(1));
    ConfigWriterFailureReceipt {
        current_command_failed: matches!(current, Err(SupervisorError::Unavailable(_))),
        helper_exec_marker: helper_spawn_marker.load(Ordering::SeqCst),
        health_ready: health.is_ready(),
        binding_drained: binding_registry_entries == 0,
        binding_registry_entries,
        cgroup_removed: !cgroup_path.exists(),
        cgroup_cleanup_count: cleanup_observer.fixture_kill_log().len(),
        deferred_cleanup_count: supervisor.deferred_cleanup_count_for_tests().await,
        local_descriptors_released: descriptor_probe.all_released(),
        subsequent_bash_rejected: matches!(subsequent, Err(SupervisorError::Unavailable(_))),
    }
}

fn fixture_cgroups(root: &Path) -> CgroupManager {
    CgroupManager::validate_fixture(root, CommandLimits::approved_defaults()).unwrap()
}

pub async fn deferred_cleanup_receipt() -> DeferredCleanupReceipt {
    let (supervisor, events, cleanup) = CommandSupervisor::test_direct_with_cleanup_probe();
    let health = supervisor.health();
    let root = FixtureRoot::new("deferred");
    let cleanup_dir = root.path().join("jail");
    std::fs::create_dir(&cleanup_dir).unwrap();
    std::fs::write(cleanup_dir.join("owned"), b"fixture").unwrap();
    let mut binding = crate::runtime_fetch_proxy::c7_test_support::held_binding(health);
    let lifecycle = binding.lifecycle.clone();
    let handle = supervisor
        .start_command_with_launch(
            CommandIdentity::new("c7", "deferred", "cleanup"),
            self_target(),
            move || {
                CommandLaunch::with_lifecycle_for_tests(Vec::new(), lifecycle)
                    .map_err(|error| error.to_string())
            },
            Duration::from_secs(5),
            1024,
            Some(cleanup_dir.clone()),
        )
        .unwrap();
    let request = tokio::time::timeout(Duration::from_millis(1_250), handle.wait()).await;
    let request_bounded = matches!(request, Ok(Err(SupervisorError::Cleanup(_))));
    let cleanup_blocked_before_receipt = cleanup_dir.exists()
        && supervisor.deferred_cleanup_count_for_tests().await == 1
        && cleanup.process_group_calls() == 0
        && cleanup.cgroup_calls() == 0
        && cleanup.jail_calls() == 0;
    binding.release();
    binding.proxy.shutdown().await.unwrap();
    supervisor.recover_stale().await.unwrap();
    DeferredCleanupReceipt {
        request_bounded,
        cleanup_blocked_before_receipt,
        cleanup_completed_after_shutdown: !cleanup_dir.exists()
            && supervisor.deferred_cleanup_count_for_tests().await == 0
            && cleanup.process_group_calls() == 1
            && cleanup.cgroup_calls() == 1
            && cleanup.jail_calls() == 1,
        markers_empty: events.lock().unwrap().is_empty(),
    }
}

pub async fn trace_receipt(cleanup_failure: bool) -> TraceReceipt {
    let (supervisor, events) = CommandSupervisor::test_direct_with_trace(cleanup_failure);
    let health = supervisor.health();
    let result = supervisor
        .start(self_target(), Vec::new(), Duration::from_secs(2))
        .unwrap()
        .wait()
        .await;
    TraceReceipt {
        events: events.lock().unwrap().clone(),
        health_ready: health.is_ready(),
        cleanup_failed: matches!(result, Err(SupervisorError::Cleanup(_))),
    }
}

fn self_target() -> ExecTarget {
    ExecTarget {
        program: std::env::current_exe().unwrap(),
        args: vec!["--list".to_string()],
        cwd: PathBuf::from("/tmp"),
    }
}

struct FixtureRoot(PathBuf);
impl FixtureRoot {
    fn new(label: &str) -> Self {
        let sequence = SEQUENCE.fetch_add(1, Ordering::Relaxed);
        let path = PathBuf::from(format!(
            "/tmp/agent-runtime-c7-{}-{sequence}-{label}",
            std::process::id()
        ));
        let _ = std::fs::remove_dir_all(&path);
        std::fs::create_dir(&path).unwrap();
        Self(path)
    }
    fn path(&self) -> &Path {
        &self.0
    }
}
impl Drop for FixtureRoot {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}
