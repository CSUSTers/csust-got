#[cfg(test)]
use super::registry::{ControlReport, GuardianReport};
use super::{RuntimeFetchProxy, RuntimeFetchProxyError, registry::BindingEntry};
use std::sync::Arc;
#[cfg(test)]
use tokio::sync::Mutex as AsyncMutex;

mod drain;
mod receipt;
mod shutdown;

pub(super) use receipt::GuardianTaskOutcome;
pub use receipt::{BindingDrainError, BindingDrainReceipt, ControlReaderOutcome, GuardianReceipt};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CommandBindingPhase {
    Active,
    Revoked,
    Drained,
}

#[derive(Clone)]
pub struct CommandLifecycleLease {
    entry: Option<Arc<BindingEntry>>,
}

impl CommandLifecycleLease {
    pub(super) fn from_entry(entry: Option<Arc<BindingEntry>>) -> Self {
        Self { entry }
    }

    pub fn detached_for_tests() -> Self {
        Self { entry: None }
    }

    #[cfg(test)]
    pub(crate) fn with_test_tasks<C, G>(control: C, guardian: G) -> Self
    where
        C: std::future::Future<Output = Result<(), &'static str>> + Send + 'static,
        G: std::future::Future<Output = Result<(usize, usize), &'static str>> + Send + 'static,
    {
        use super::registry::BindingContext;
        use crate::workspace_budget::WorkspaceBudget;
        use std::{path::PathBuf, sync::Weak};
        use tokio_util::sync::CancellationToken;

        let root = tempfile::tempdir().expect("test workspace").keep();
        let workspace_budget = WorkspaceBudget::new(&root, 1024).expect("test budget");
        let control_reader = tokio::spawn(async move {
            control
                .await
                .map(|()| ControlReport::default())
                .map_err(RuntimeFetchProxyError::new)
        });
        let guardian = tokio::spawn(async move {
            guardian
                .await
                .map(|(spawned_sessions, joined_sessions)| GuardianReport {
                    spawned_sessions,
                    joined_sessions,
                    joinset_empty: true,
                    job_channel_closed: true,
                })
                .map_err(RuntimeFetchProxyError::new)
        });
        let entry = Arc::new(BindingEntry {
            command_id: "test-command".to_string(),
            context: Arc::new(BindingContext {
                phase: Arc::new(std::sync::Mutex::new(CommandBindingPhase::Active)),
                namespace: "test-namespace".to_string(),
                workspace_budget,
                health: crate::exec::BashHealth::ready(),
                issued: None,
                broker_socket: Some(PathBuf::from("unused")),
                #[cfg(target_os = "linux")]
                session_permits: Arc::new(tokio::sync::Semaphore::new(
                    super::MAX_ACTIVE_LOCAL_SESSIONS,
                )),
            }),
            control_cancel: CancellationToken::new(),
            guardian_cancel: CancellationToken::new(),
            control_reader: AsyncMutex::new(Some(control_reader)),
            control_outcome: AsyncMutex::new(None),
            guardian: AsyncMutex::new(Some(guardian)),
            guardian_outcome: AsyncMutex::new(None),
            #[cfg(target_os = "linux")]
            job_sender: std::sync::Mutex::new(None),
            proxy: Weak::new(),
        });
        Self::from_entry(Some(entry))
    }

    pub fn request_revoke(&self) {
        let Some(entry) = &self.entry else {
            return;
        };
        if let Ok(mut phase) = entry.context.phase.lock()
            && *phase == CommandBindingPhase::Active
        {
            *phase = CommandBindingPhase::Revoked;
            entry.control_cancel.cancel();
            entry.guardian_cancel.cancel();
        }
    }

    pub fn phase(&self) -> Result<Option<CommandBindingPhase>, RuntimeFetchProxyError> {
        self.entry
            .as_ref()
            .map(|entry| {
                entry
                    .context
                    .phase
                    .lock()
                    .map(|phase| *phase)
                    .map_err(|_| RuntimeFetchProxyError::new("command binding phase is poisoned"))
            })
            .transpose()
    }

    pub async fn revoke_and_wait(&self) -> Result<BindingDrainReceipt, BindingDrainError> {
        self.revoke_and_wait_with_slow_observer(|| {}).await
    }

    pub(crate) async fn revoke_and_wait_with_slow_observer(
        &self,
        on_slow_owner: impl FnOnce(),
    ) -> Result<BindingDrainReceipt, BindingDrainError> {
        let Some(entry) = &self.entry else {
            return Ok(BindingDrainReceipt {
                control_reader: ControlReaderOutcome::Completed,
                guardian: GuardianReceipt {
                    spawned_sessions: 0,
                    joined_sessions: 0,
                    joinset_empty: true,
                    job_channel_closed: true,
                },
            });
        };
        {
            let mut phase = entry
                .context
                .phase
                .lock()
                .map_err(|_| BindingDrainError::StateUnavailable)?;
            match *phase {
                CommandBindingPhase::Active => *phase = CommandBindingPhase::Revoked,
                CommandBindingPhase::Revoked => {}
                CommandBindingPhase::Drained => {}
            }
            entry.control_cancel.cancel();
            entry.guardian_cancel.cancel();
        }
        let mut slow_observer = Some(on_slow_owner);
        let control_result = drain::await_control_reader(entry, &mut slow_observer).await;

        #[cfg(target_os = "linux")]
        let sender_error = match entry.job_sender.lock() {
            Ok(mut sender) => {
                drop(sender.take());
                None
            }
            Err(_) => Some(BindingDrainError::StateUnavailable),
        };
        let guardian_result = drain::await_guardian(entry, &mut slow_observer).await;

        let result = (|| {
            let guardian = match guardian_result? {
                GuardianTaskOutcome::Success(receipt) => receipt,
                GuardianTaskOutcome::Error
                | GuardianTaskOutcome::Panicked
                | GuardianTaskOutcome::Cancelled => {
                    return Err(BindingDrainError::GuardianFailed);
                }
            };
            if !guardian.is_exact() {
                return Err(BindingDrainError::ReceiptMismatch);
            }
            #[cfg(target_os = "linux")]
            if let Some(error) = sender_error {
                return Err(error);
            }
            let control_reader = control_result?;
            Ok(BindingDrainReceipt {
                control_reader,
                guardian,
            })
        })();
        let receipt = match result {
            Ok(receipt) => receipt,
            Err(error) => {
                entry.context.health.latch_binding_drain_failure();
                return Err(error);
            }
        };
        let finalize = (|| {
            if let Some(proxy) = entry.proxy.upgrade() {
                let mut registry = proxy
                    .registry
                    .lock()
                    .map_err(|_| BindingDrainError::StateUnavailable)?;
                let mut phase = entry
                    .context
                    .phase
                    .lock()
                    .map_err(|_| BindingDrainError::StateUnavailable)?;
                if registry
                    .get(&entry.command_id)
                    .is_some_and(|registered| Arc::ptr_eq(registered, entry))
                {
                    registry.remove(&entry.command_id);
                }
                *phase = CommandBindingPhase::Drained;
            } else {
                let mut phase = entry
                    .context
                    .phase
                    .lock()
                    .map_err(|_| BindingDrainError::StateUnavailable)?;
                *phase = CommandBindingPhase::Drained;
            }
            Ok(())
        })();
        if let Err(error) = finalize {
            entry.context.health.latch_binding_drain_failure();
            return Err(error);
        }
        Ok(receipt)
    }
}

#[cfg(test)]
mod test_support;
#[cfg(test)]
mod tests;
