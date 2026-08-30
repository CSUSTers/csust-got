use super::{CommandCgroup, supervision};
use std::{
    io,
    path::PathBuf,
    sync::{
        Arc, Mutex,
        atomic::{AtomicUsize, Ordering},
    },
    time::Duration,
};
use tokio::{process::Child, task::JoinHandle};

const DEFERRED_CLEANUP_STEP_TIMEOUT: Duration = Duration::from_secs(1);

#[derive(Clone, Default)]
pub(super) struct DeferredCleanupRegistry {
    entries: Arc<Mutex<Vec<DeferredCommandCleanup>>>,
}

pub(super) struct DeferredCommandCleanup {
    pub(super) child: Option<Child>,
    pub(super) child_id: Option<u32>,
    pub(super) cgroup: Option<CommandCgroup>,
    pub(super) stdout: Option<JoinHandle<io::Result<supervision::CollectedOutput>>>,
    pub(super) stderr: Option<JoinHandle<io::Result<supervision::CollectedOutput>>>,
    pub(super) cleanup_dir: Option<PathBuf>,
    pub(super) probe: Option<Arc<CleanupProbe>>,
    process_group_cleaned: bool,
    cgroup_recovered: bool,
    child_reaped: bool,
}

impl DeferredCommandCleanup {
    pub(super) fn new_running(
        child: Child,
        child_id: Option<u32>,
        cgroup: Option<CommandCgroup>,
        stdout: JoinHandle<io::Result<supervision::CollectedOutput>>,
        stderr: JoinHandle<io::Result<supervision::CollectedOutput>>,
        cleanup_dir: Option<PathBuf>,
        probe: Option<Arc<CleanupProbe>>,
    ) -> Self {
        Self {
            child: Some(child),
            child_id,
            cgroup,
            stdout: Some(stdout),
            stderr: Some(stderr),
            cleanup_dir,
            probe,
            process_group_cleaned: false,
            cgroup_recovered: false,
            child_reaped: false,
        }
    }

    #[cfg(target_os = "linux")]
    pub(super) fn new_unlaunched(
        cgroup: Option<CommandCgroup>,
        cleanup_dir: Option<PathBuf>,
        probe: Option<Arc<CleanupProbe>>,
    ) -> Self {
        Self {
            child: None,
            child_id: None,
            cgroup,
            stdout: None,
            stderr: None,
            cleanup_dir,
            probe,
            process_group_cleaned: true,
            cgroup_recovered: false,
            child_reaped: true,
        }
    }

    async fn recover(&mut self) -> Result<bool, String> {
        if !self.cgroup_recovered {
            self.cgroup.take();
            self.cgroup_recovered = true;
            if let Some(probe) = &self.probe {
                probe.cgroup_calls.fetch_add(1, Ordering::SeqCst);
            }
        }
        if !self.process_group_cleaned {
            if let Some(probe) = &self.probe {
                probe.process_group_calls.fetch_add(1, Ordering::SeqCst);
            }
            supervision::cleanup_command_process_group(self.child_id)?;
            self.process_group_cleaned = true;
        }
        if !self.child_reaped {
            let child = self
                .child
                .as_mut()
                .ok_or_else(|| "retained command child is missing".to_string())?;
            if child.try_wait().map_err(redacted_wait_error)?.is_none() {
                tokio::time::timeout(DEFERRED_CLEANUP_STEP_TIMEOUT, child.wait())
                    .await
                    .map_err(|_| "retained command wait exceeded cleanup deadline".to_string())?
                    .map_err(redacted_wait_error)?;
            }
            self.child_reaped = true;
            self.child.take();
        }
        await_collector(&mut self.stdout, "stdout").await?;
        await_collector(&mut self.stderr, "stderr").await?;
        if let Some(directory) = self.cleanup_dir.as_deref() {
            if let Some(probe) = &self.probe {
                probe.jail_calls.fetch_add(1, Ordering::SeqCst);
            }
            supervision::cleanup_directory(Some(directory))
                .await
                .map_err(|_| "retained command jail cleanup failed".to_string())?;
            self.cleanup_dir = None;
        }
        Ok(true)
    }
}

impl DeferredCleanupRegistry {
    pub(super) fn retain(&self, cleanup: DeferredCommandCleanup) {
        self.entries
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
            .push(cleanup);
    }

    pub(super) async fn recover_all(&self) -> Result<(), String> {
        let pending = {
            let mut entries = self
                .entries
                .lock()
                .unwrap_or_else(std::sync::PoisonError::into_inner);
            std::mem::take(&mut *entries)
        };
        let mut retained = Vec::new();
        for mut cleanup in pending {
            if cleanup.recover().await.is_err() {
                retained.push(cleanup);
            }
        }
        let failures = retained.len();
        self.entries
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
            .extend(retained);
        if failures == 0 {
            Ok(())
        } else {
            Err(format!(
                "retained command cleanup failed for {failures} command(s)"
            ))
        }
    }

    #[cfg(any(test, feature = "c7-test-support"))]
    pub(super) fn len(&self) -> usize {
        self.entries
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
            .len()
    }
}

async fn await_collector(
    slot: &mut Option<JoinHandle<io::Result<supervision::CollectedOutput>>>,
    stream: &str,
) -> Result<(), String> {
    let Some(task) = slot.as_mut() else {
        return Ok(());
    };
    tokio::time::timeout(DEFERRED_CLEANUP_STEP_TIMEOUT, &mut *task)
        .await
        .map_err(|_| format!("retained command {stream} drain exceeded cleanup deadline"))?
        .map_err(|_| format!("retained command {stream} collector failed"))?
        .map_err(|_| format!("retained command {stream} drain failed"))?;
    slot.take();
    Ok(())
}

fn redacted_wait_error(_: io::Error) -> String {
    "retained command wait failed".to_string()
}

#[derive(Default)]
pub(super) struct CleanupProbe {
    process_group_calls: AtomicUsize,
    cgroup_calls: AtomicUsize,
    jail_calls: AtomicUsize,
    #[cfg(any(test, feature = "c7-test-support"))]
    stale_recovery_calls: AtomicUsize,
}

impl CleanupProbe {
    pub(super) fn record_process_group(&self) {
        self.process_group_calls.fetch_add(1, Ordering::SeqCst);
    }

    pub(super) fn record_cgroup(&self) {
        self.cgroup_calls.fetch_add(1, Ordering::SeqCst);
    }

    pub(super) fn record_jail(&self) {
        self.jail_calls.fetch_add(1, Ordering::SeqCst);
    }

    #[cfg(any(test, feature = "c7-test-support"))]
    pub(super) fn record_stale_recovery(&self) {
        self.stale_recovery_calls.fetch_add(1, Ordering::SeqCst);
    }
}

#[cfg(any(test, feature = "c7-test-support"))]
impl CleanupProbe {
    pub(super) fn process_group_calls(&self) -> usize {
        self.process_group_calls.load(Ordering::SeqCst)
    }

    pub(super) fn cgroup_calls(&self) -> usize {
        self.cgroup_calls.load(Ordering::SeqCst)
    }

    pub(super) fn jail_calls(&self) -> usize {
        self.jail_calls.load(Ordering::SeqCst)
    }

    #[cfg(test)]
    pub(super) fn stale_recovery_calls(&self) -> usize {
        self.stale_recovery_calls.load(Ordering::SeqCst)
    }
}
