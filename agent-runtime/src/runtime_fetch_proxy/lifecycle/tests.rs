use super::*;
use crate::{
    runtime_fetch_proxy::registry::{BindingContext, ControlReport, GuardianReport},
    workspace_budget::WorkspaceBudget,
};
use std::{
    path::PathBuf,
    sync::{Arc, Mutex},
    time::Duration,
};
use tokio::sync::{Mutex as AsyncMutex, oneshot};
use tokio_util::sync::CancellationToken;

#[tokio::test]
async fn c7_guardian_timeout_retains_entry_handles_and_joinset() {
    let (release_tx, release_rx) = oneshot::channel();
    let control = tokio::spawn(async { Ok(ControlReport) });
    let guardian = tokio::spawn(async move {
        release_rx
            .await
            .map_err(|_| RuntimeFetchProxyError::new("test guardian release channel was closed"))?;
        Ok(exact_guardian_receipt(1))
    });
    let (proxy, lease, entry) = test_proxy_lease(control, guardian);
    let guardian_id = entry
        .guardian
        .lock()
        .await
        .as_ref()
        .expect("guardian handle")
        .id();
    let (slow_tx, slow_rx) = oneshot::channel();

    let error = tokio::time::timeout(
        Duration::from_millis(1_250),
        lease.revoke_and_wait_with_slow_observer(|| {
            let _ = slow_tx.send(());
        }),
    )
    .await
    .expect("drain must return within its strict bound")
    .unwrap_err();

    slow_rx.await.unwrap();
    assert!(matches!(error, BindingDrainError::DrainPending));
    assert_eq!(lease.phase().unwrap(), Some(CommandBindingPhase::Revoked));
    assert_eq!(proxy.active_binding_count().unwrap(), 1);
    assert_eq!(
        entry
            .guardian
            .lock()
            .await
            .as_ref()
            .expect("same retained guardian handle")
            .id(),
        guardian_id
    );

    release_tx.send(()).unwrap();
    proxy.shutdown().await.unwrap();

    assert_eq!(proxy.active_binding_count().unwrap(), 0);
    assert_eq!(lease.phase().unwrap(), Some(CommandBindingPhase::Drained));
}

#[tokio::test]
async fn c7_control_reader_panic_still_drains_guardian() {
    let control = tokio::spawn(async {
        panic!("deterministic control reader panic");
        #[allow(unreachable_code)]
        Ok(ControlReport)
    });
    let guardian = tokio::spawn(async { Ok(exact_guardian_receipt(2)) });
    let (proxy, lease, _) = test_proxy_lease(control, guardian);

    let receipt = lease.revoke_and_wait().await.unwrap();

    assert_eq!(receipt.control_reader, ControlReaderOutcome::Panicked);
    assert_eq!(receipt.guardian.spawned_sessions, 2);
    assert_eq!(receipt.guardian.joined_sessions, 2);
    assert!(receipt.guardian.joinset_empty);
    assert!(receipt.guardian.job_channel_closed);
    assert_eq!(lease.phase().unwrap(), Some(CommandBindingPhase::Drained));
    assert_eq!(proxy.active_binding_count().unwrap(), 0);
}

#[tokio::test]
async fn c7_guardian_receipt_mismatch_blocks_cgroup_cleanup() {
    let control = tokio::spawn(async { Ok(ControlReport) });
    let guardian = tokio::spawn(async {
        Ok(GuardianReport {
            spawned_sessions: 2,
            joined_sessions: 1,
            joinset_empty: true,
            job_channel_closed: true,
        })
    });
    let (proxy, lease, entry) = test_proxy_lease(control, guardian);

    let error = lease.revoke_and_wait().await.unwrap_err();

    assert!(matches!(error, BindingDrainError::ReceiptMismatch));
    assert_eq!(lease.phase().unwrap(), Some(CommandBindingPhase::Revoked));
    assert_eq!(proxy.active_binding_count().unwrap(), 1);
    assert!(entry.guardian.lock().await.is_none());
    assert!(entry.guardian_outcome.lock().await.is_some());
}

#[tokio::test]
async fn control_reader_error_still_yields_valid_receipt_after_exact_guardian_drain() {
    let control = tokio::spawn(async {
        Err(RuntimeFetchProxyError::new(
            "deterministic control reader error",
        ))
    });
    let guardian = tokio::spawn(async { Ok(exact_guardian_receipt(1)) });
    let (_, lease, _) = test_proxy_lease(control, guardian);

    let receipt = lease.revoke_and_wait().await.unwrap();

    assert_eq!(receipt.control_reader, ControlReaderOutcome::Error);
    assert_eq!(receipt.guardian.spawned_sessions, 1);
    assert_eq!(receipt.guardian.joined_sessions, 1);
}

fn exact_guardian_receipt(sessions: usize) -> GuardianReport {
    GuardianReport {
        spawned_sessions: sessions,
        joined_sessions: sessions,
        joinset_empty: true,
        job_channel_closed: true,
    }
}

fn test_proxy_lease(
    control_reader: tokio::task::JoinHandle<Result<ControlReport, RuntimeFetchProxyError>>,
    guardian: tokio::task::JoinHandle<Result<GuardianReport, RuntimeFetchProxyError>>,
) -> (RuntimeFetchProxy, CommandLifecycleLease, Arc<BindingEntry>) {
    let root = tempfile::tempdir().unwrap().keep();
    let workspace_budget = WorkspaceBudget::new(&root, 1024).unwrap();
    let proxy = RuntimeFetchProxy::disabled(workspace_budget.clone());
    let entry = Arc::new(BindingEntry {
        command_id: "test-command".to_string(),
        context: Arc::new(BindingContext {
            phase: Arc::new(Mutex::new(CommandBindingPhase::Active)),
            namespace: "test-namespace".to_string(),
            workspace_budget,
            health: crate::exec::BashHealth::ready(),
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
        .unwrap()
        .insert(entry.command_id.clone(), Arc::clone(&entry));
    let lease = CommandLifecycleLease::from_entry(Some(Arc::clone(&entry)));
    (proxy, lease, entry)
}
