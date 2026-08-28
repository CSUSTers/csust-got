use super::SupervisorError;
use std::{
    collections::HashMap,
    sync::{Arc, Mutex},
};
use tokio::sync::{Notify, watch};

pub(super) const SHUTDOWN_REASON: &str = "bash unavailable: runtime is shutting down";
const CLEANUP_REASON: &str = "bash unavailable: command cleanup failed";
#[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
const ENFORCEMENT_REASON: &str = "bash unavailable: command enforcement failed";
const DURABILITY_REASON: &str = "bash unavailable: workspace durability failed";

#[derive(Clone)]
pub struct BashHealth {
    inner: Arc<HealthInner>,
}

struct HealthInner {
    state: Mutex<HealthState>,
    drained: Notify,
}

struct HealthState {
    reason: Option<String>,
    cleanup_failed: bool,
    #[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
    next_command_id: u64,
    active: HashMap<u64, watch::Sender<bool>>,
}

#[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
pub(crate) struct ActiveCommandLease {
    command_id: Option<u64>,
    inner: Arc<HealthInner>,
}

impl BashHealth {
    pub fn ready() -> Self {
        Self::with_reason(None)
    }

    pub fn unavailable(reason: impl Into<String>) -> Self {
        Self::with_reason(Some(reason.into()))
    }

    fn with_reason(reason: Option<String>) -> Self {
        Self {
            inner: Arc::new(HealthInner {
                state: Mutex::new(HealthState {
                    reason,
                    cleanup_failed: false,
                    #[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
                    next_command_id: 0,
                    active: HashMap::new(),
                }),
                drained: Notify::new(),
            }),
        }
    }

    pub fn is_ready(&self) -> bool {
        self.inner
            .state
            .lock()
            .is_ok_and(|state| state.reason.is_none())
    }

    pub fn reason(&self) -> String {
        self.inner
            .state
            .lock()
            .ok()
            .and_then(|state| state.reason.clone())
            .unwrap_or_else(|| "bash unavailable: health state is poisoned".to_string())
    }

    pub fn active_command_count(&self) -> usize {
        self.inner
            .state
            .lock()
            .map(|state| state.active.len())
            .unwrap_or(0)
    }

    #[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
    pub(crate) fn register(
        &self,
        cancel: watch::Sender<bool>,
    ) -> Result<ActiveCommandLease, SupervisorError> {
        let mut state = self.inner.state.lock().map_err(|_| {
            SupervisorError::Unavailable("bash unavailable: health state is poisoned".to_string())
        })?;
        if let Some(reason) = &state.reason {
            return Err(SupervisorError::Unavailable(reason.clone()));
        }
        let command_id = state.next_command_id;
        state.next_command_id = state.next_command_id.wrapping_add(1);
        state.active.insert(command_id, cancel);
        Ok(ActiveCommandLease {
            command_id: Some(command_id),
            inner: Arc::clone(&self.inner),
        })
    }

    pub(super) fn begin_shutdown(&self) {
        self.latch_and_cancel(SHUTDOWN_REASON, false);
    }

    #[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
    pub(super) fn latch_cleanup_failure(&self) {
        self.latch_and_cancel(CLEANUP_REASON, true);
    }

    pub(crate) fn latch_binding_drain_failure(&self) {
        self.latch_and_cancel(CLEANUP_REASON, true);
    }

    #[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
    pub(crate) fn latch_enforcement_failure(&self) {
        self.latch_and_cancel(ENFORCEMENT_REASON, true);
    }

    pub(crate) fn latch_workspace_durability_failure(&self) {
        self.latch(DURABILITY_REASON, true, false);
    }

    fn latch_and_cancel(&self, reason: &str, cleanup_failed: bool) {
        self.latch(reason, cleanup_failed, true);
    }

    fn latch(&self, reason: &str, cleanup_failed: bool, cancel_active: bool) {
        let Ok(mut state) = self.inner.state.lock() else {
            return;
        };
        if state.reason.is_none() {
            state.reason = Some(reason.to_string());
        }
        state.cleanup_failed |= cleanup_failed;
        if cancel_active {
            for cancel in state.active.values() {
                let _ = cancel.send(true);
            }
        }
    }

    pub(super) async fn wait_for_drain(&self) -> Result<(), SupervisorError> {
        loop {
            let notified = self.inner.drained.notified();
            let (empty, cleanup_failed) = self
                .inner
                .state
                .lock()
                .map(|state| (state.active.is_empty(), state.cleanup_failed))
                .map_err(|_| {
                    SupervisorError::Cleanup(
                        "command health state was poisoned during shutdown".to_string(),
                    )
                })?;
            if empty {
                return if cleanup_failed {
                    Err(SupervisorError::Cleanup(
                        "command cleanup failed during shutdown".to_string(),
                    ))
                } else {
                    Ok(())
                };
            }
            notified.await;
        }
    }
}

#[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
impl Drop for ActiveCommandLease {
    fn drop(&mut self) {
        let Some(command_id) = self.command_id.take() else {
            return;
        };
        let Ok(mut state) = self.inner.state.lock() else {
            return;
        };
        state.active.remove(&command_id);
        if state.active.is_empty() {
            self.inner.drained.notify_waiters();
        }
    }
}
