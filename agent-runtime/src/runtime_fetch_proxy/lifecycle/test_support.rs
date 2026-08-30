use super::*;
use crate::{
    exec::BashHealth,
    runtime_fetch_proxy::registry::{BindingContext, ControlReport, GuardianReport},
    workspace_budget::WorkspaceBudget,
};
use std::{path::PathBuf, sync::Mutex};
use tokio::sync::Mutex as AsyncMutex;
use tokio_util::sync::CancellationToken;

impl RuntimeFetchProxy {
    pub(crate) fn with_test_binding_for_tests<C, G>(
        health: BashHealth,
        control: C,
        guardian: G,
    ) -> (Self, CommandLifecycleLease)
    where
        C: std::future::Future<Output = Result<(), &'static str>> + Send + 'static,
        G: std::future::Future<Output = Result<(usize, usize), &'static str>> + Send + 'static,
    {
        let root = tempfile::tempdir().expect("test workspace").keep();
        let workspace_budget = WorkspaceBudget::new(&root, 1024).expect("test budget");
        let proxy = RuntimeFetchProxy::disabled(workspace_budget.clone());
        let control_reader = tokio::spawn(async move {
            control
                .await
                .map(|()| ControlReport)
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
                phase: Arc::new(Mutex::new(CommandBindingPhase::Active)),
                namespace: "test-namespace".to_string(),
                workspace_budget,
                health,
                issued: None,
                broker_socket: Some(PathBuf::from("unused")),
                #[cfg(target_os = "linux")]
                session_permits: Arc::new(tokio::sync::Semaphore::new(
                    crate::runtime_fetch_proxy::MAX_ACTIVE_LOCAL_SESSIONS,
                )),
            }),
            control_cancel: CancellationToken::new(),
            guardian_cancel: CancellationToken::new(),
            control_reader: AsyncMutex::new(Some(control_reader)),
            control_outcome: AsyncMutex::new(None),
            guardian: AsyncMutex::new(Some(guardian)),
            guardian_outcome: AsyncMutex::new(None),
            #[cfg(target_os = "linux")]
            job_sender: Mutex::new(None),
            proxy: Arc::downgrade(&proxy.inner),
        });
        proxy
            .inner
            .registry
            .lock()
            .expect("test registry")
            .insert(entry.command_id.clone(), Arc::clone(&entry));
        let lifecycle = CommandLifecycleLease::from_entry(Some(entry));
        (proxy, lifecycle)
    }
}
