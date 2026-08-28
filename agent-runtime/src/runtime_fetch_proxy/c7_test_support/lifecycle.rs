mod fixture;
mod guardian_tasks;

pub(super) use guardian_tasks::guardian_task_evidence;

use super::super::{
    BindingDrainError, BindingDrainReceipt, CommandBindingPhase, CommandLifecycleLease,
    RuntimeFetchProxy, RuntimeFetchProxyError,
    lifecycle::GuardianTaskOutcome,
    registry::{BindingEntry, ControlReport},
};
use crate::exec::{BashHealth, CommandSupervisor};
use fixture::{
    GuardianMode, binding_fixture, enqueue_blocking_pair, enqueue_pending, workspace_budget,
};
use std::{sync::Arc, time::Duration};
use tokio::sync::oneshot;

pub struct GuardianMismatchReceipt {
    pub error: BindingDrainError,
    pub phase: CommandBindingPhase,
    pub registry_entries: usize,
    pub health_ready: bool,
}

pub struct GuardianTimeoutReceipt {
    pub error: BindingDrainError,
    pub same_handle_retained: bool,
    pub registry_before_release: usize,
    pub registry_after_shutdown: usize,
    pub phase_before_release: CommandBindingPhase,
    pub live_sessions_before_revoke: usize,
    pub live_sessions_after_timeout: usize,
    pub permits_before_revoke: usize,
    pub permits_after_timeout: usize,
    pub cleanup_authorized_before_release: bool,
    pub guardian_spawned_after_shutdown: usize,
    pub guardian_joined_after_shutdown: usize,
    pub guardian_joinset_empty_after_shutdown: bool,
    pub permits_after_shutdown: usize,
    pub live_sessions_after_shutdown: usize,
}

pub struct ControlPanicReceipt {
    pub drain: BindingDrainReceipt,
    pub live_sessions_before_panic: usize,
    pub permits_while_live: usize,
    pub permits_after_receipts: usize,
    pub cleanup_authorized: bool,
}

pub struct SharedHealthReceipt {
    pub supervisor_ready: bool,
    pub binding_ready: bool,
    pub same_reason: bool,
}

pub struct DeferredBinding {
    pub proxy: RuntimeFetchProxy,
    pub lifecycle: CommandLifecycleLease,
    _entry: Arc<BindingEntry>,
    release: Option<oneshot::Sender<()>>,
}

impl DeferredBinding {
    pub fn release(&mut self) {
        if let Some(release) = self.release.take() {
            let _ = release.send(());
        }
    }
}

pub async fn control_panic_receipt() -> ControlPanicReceipt {
    let (panic_tx, panic_rx) = oneshot::channel();
    let fixture = binding_fixture(
        BashHealth::ready(),
        async move {
            let _ = panic_rx.await;
            panic!("c7 control reader panic");
            #[allow(unreachable_code)]
            Ok(ControlReport)
        },
        GuardianMode::Exact,
    );
    assert_eq!(enqueue_pending(&fixture).await, 1);
    assert_eq!(enqueue_pending(&fixture).await, 0);
    let _ = panic_tx.send(());
    let drain = fixture.lifecycle.revoke_and_wait().await.unwrap();
    let registry_entries = fixture.proxy.active_binding_count().unwrap();
    ControlPanicReceipt {
        drain,
        live_sessions_before_panic: 2,
        permits_while_live: 0,
        permits_after_receipts: fixture.available_permits(),
        cleanup_authorized: registry_entries == 0
            && fixture.lifecycle.phase().unwrap() == Some(CommandBindingPhase::Drained),
    }
}

pub async fn guardian_mismatch_receipt() -> GuardianMismatchReceipt {
    let health = BashHealth::ready();
    let fixture = binding_fixture(
        health.clone(),
        async { Ok(ControlReport) },
        GuardianMode::Mismatch,
    );
    assert_eq!(enqueue_pending(&fixture).await, 1);
    let error = fixture.lifecycle.revoke_and_wait().await.unwrap_err();
    GuardianMismatchReceipt {
        error,
        phase: fixture.lifecycle.phase().unwrap().unwrap(),
        registry_entries: fixture.proxy.active_binding_count().unwrap(),
        health_ready: health.is_ready(),
    }
}

pub async fn guardian_timeout_receipt() -> GuardianTimeoutReceipt {
    let fixture = binding_fixture(
        BashHealth::ready(),
        async { Ok(ControlReport) },
        GuardianMode::Exact,
    );
    let blocking = enqueue_blocking_pair(&fixture).await;
    let entry = Arc::clone(&fixture.entry);
    let handle_id = entry.guardian.lock().await.as_ref().unwrap().id();
    let live_sessions_before_revoke = blocking.live();
    let permits_before_revoke = fixture.available_permits();
    let error = fixture.lifecycle.revoke_and_wait().await.unwrap_err();
    let same_handle_retained = entry.guardian.lock().await.as_ref().unwrap().id() == handle_id;
    let phase_before_release = fixture.lifecycle.phase().unwrap().unwrap();
    let registry_before_release = fixture.proxy.active_binding_count().unwrap();
    let live_sessions_after_timeout = blocking.live();
    let permits_after_timeout = fixture.available_permits();
    let cleanup_authorized_before_release =
        phase_before_release == CommandBindingPhase::Drained || registry_before_release == 0;
    blocking.release();
    fixture.proxy.shutdown().await.unwrap();
    let guardian = match *entry.guardian_outcome.lock().await {
        Some(GuardianTaskOutcome::Success(receipt)) => receipt,
        _ => panic!("guardian did not produce an exact receipt"),
    };
    GuardianTimeoutReceipt {
        error,
        same_handle_retained,
        registry_before_release,
        registry_after_shutdown: fixture.proxy.active_binding_count().unwrap(),
        phase_before_release,
        live_sessions_before_revoke,
        live_sessions_after_timeout,
        permits_before_revoke,
        permits_after_timeout,
        cleanup_authorized_before_release,
        guardian_spawned_after_shutdown: guardian.spawned_sessions,
        guardian_joined_after_shutdown: guardian.joined_sessions,
        guardian_joinset_empty_after_shutdown: guardian.joinset_empty,
        permits_after_shutdown: fixture.available_permits(),
        live_sessions_after_shutdown: blocking.live(),
    }
}

pub async fn shared_supervisor_health_receipt() -> SharedHealthReceipt {
    let supervisor = CommandSupervisor::test_direct();
    let health = supervisor.health();
    let proxy = RuntimeFetchProxy::disabled(workspace_budget("shared-health"));
    let launch = proxy
        .bind_command(
            "c7",
            "shared-health",
            "shared-health".to_string(),
            Duration::from_secs(1),
            health.clone(),
        )
        .unwrap()
        .into_launch(proxy.shell_environment())
        .unwrap();
    let entry = proxy
        .inner
        .registry
        .lock()
        .unwrap()
        .get("shared-health")
        .unwrap()
        .clone();
    let guardian = entry.guardian.try_lock().unwrap().take().unwrap();
    *entry.guardian.try_lock().unwrap() = Some(tokio::spawn(async move {
        let mut receipt = guardian
            .await
            .map_err(|_| RuntimeFetchProxyError::new("join"))??;
        receipt.spawned_sessions = 1;
        receipt.joined_sessions = 0;
        Ok(receipt)
    }));
    let _ = launch.lifecycle.revoke_and_wait().await;
    SharedHealthReceipt {
        supervisor_ready: supervisor.health().is_ready(),
        binding_ready: entry.context.health.is_ready(),
        same_reason: supervisor.health().reason() == entry.context.health.reason(),
    }
}

pub(crate) fn held_binding(health: BashHealth) -> DeferredBinding {
    let (release_tx, release_rx) = oneshot::channel();
    let fixture = binding_fixture(
        health,
        async { Ok(ControlReport) },
        GuardianMode::Hold(release_rx),
    );
    DeferredBinding {
        proxy: fixture.proxy,
        lifecycle: fixture.lifecycle,
        _entry: fixture.entry,
        release: Some(release_tx),
    }
}
