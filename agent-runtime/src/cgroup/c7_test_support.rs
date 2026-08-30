use super::{CgroupManager, CommandIdentity, CommandLimits};
use crate::exec::BashHealth;
use std::{
    fs,
    path::{Path, PathBuf},
    sync::atomic::{AtomicU64, Ordering},
};
use tokio::sync::watch;

static FIXTURE_SEQUENCE: AtomicU64 = AtomicU64::new(0);
pub const LIMIT_CONTROLS: [&str; 5] = [
    "pids.max",
    "memory.max",
    "memory.swap.max",
    "memory.oom.group",
    "cpu.max",
];

pub struct EnforcementReceipt {
    pub health: BashHealth,
    pub active_cancelled: bool,
    pub operation_failed: bool,
}

pub fn create_failure() -> EnforcementReceipt {
    let root = FixtureRoot::new("create");
    let manager = fixture_manager(root.path(), None);
    let identity = CommandIdentity::new("c7", "create", "failure");
    fs::write(root.path().join(identity.cgroup_name()), b"collision").unwrap();
    latch(manager.create(&identity).is_err())
}

pub fn limit_control_failure(control: &'static str) -> EnforcementReceipt {
    assert!(LIMIT_CONTROLS.contains(&control));
    let root = FixtureRoot::new(control);
    let manager = fixture_manager(root.path(), Some(control));
    latch(
        manager
            .create(&CommandIdentity::new("c7", "control", control))
            .is_err(),
    )
}

pub fn cpu_usage_failure(parse_failure: bool) -> EnforcementReceipt {
    let root = FixtureRoot::new(if parse_failure {
        "cpu-parse"
    } else {
        "cpu-read"
    });
    let manager = fixture_manager(root.path(), None);
    let group = manager
        .create(&CommandIdentity::new("c7", "cpu", "failure"))
        .unwrap();
    if parse_failure {
        fs::write(group.path().join("cpu.stat"), "usage_usec invalid\n").unwrap();
    }
    latch(group.cpu_usage().is_err())
}

pub fn irreversible_health() -> BashHealth {
    let receipt = create_failure();
    receipt.health.latch_workspace_durability_failure();
    receipt.health
}

fn latch(operation_failed: bool) -> EnforcementReceipt {
    let health = BashHealth::ready();
    let (cancel, receiver) = watch::channel(false);
    let _active = health.register(cancel).unwrap();
    if operation_failed {
        health.latch_enforcement_failure();
    }
    EnforcementReceipt {
        active_cancelled: *receiver.borrow(),
        health,
        operation_failed,
    }
}

fn fixture_manager(root: &Path, fail: Option<&'static str>) -> CgroupManager {
    fs::write(root.join("cgroup.controllers"), "pids memory cpu").unwrap();
    fs::write(root.join("cgroup.subtree_control"), "pids memory cpu").unwrap();
    fs::write(root.join("cgroup.procs"), "").unwrap();
    match fail {
        Some(control) => CgroupManager::validate_fixture_with_control_failure(
            root,
            CommandLimits::approved_defaults(),
            control,
        ),
        None => CgroupManager::validate_fixture(root, CommandLimits::approved_defaults()),
    }
    .unwrap()
}

struct FixtureRoot(PathBuf);

impl FixtureRoot {
    fn new(label: &str) -> Self {
        let sequence = FIXTURE_SEQUENCE.fetch_add(1, Ordering::Relaxed);
        let path = PathBuf::from(format!(
            "/tmp/agent-runtime-c7-{}-{sequence}-{}",
            std::process::id(),
            label.replace(['/', '.'], "-")
        ));
        let _ = fs::remove_dir_all(&path);
        fs::create_dir(&path).unwrap();
        Self(path)
    }

    fn path(&self) -> &Path {
        &self.0
    }
}

impl Drop for FixtureRoot {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}
