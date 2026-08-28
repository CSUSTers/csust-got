use super::{
    BindingDrainError,
    fixture::{GuardianMode, binding_fixture, enqueue_signaled_fault, enqueue_valid_session},
};
use crate::{
    exec::BashHealth,
    runtime_fetch_proxy::{control::SessionFault, registry::ControlReport},
};
use std::sync::Arc;

pub(in crate::runtime_fetch_proxy::c7_test_support) struct GuardianTaskEvidence {
    pub(in crate::runtime_fetch_proxy::c7_test_support) valid_exact: bool,
    pub(in crate::runtime_fetch_proxy::c7_test_support) panic_rejected: bool,
    pub(in crate::runtime_fetch_proxy::c7_test_support) unclassified_rejected: bool,
}

pub(in crate::runtime_fetch_proxy::c7_test_support) async fn guardian_task_evidence()
-> GuardianTaskEvidence {
    let valid = binding_fixture(
        BashHealth::ready(),
        async { Ok(ControlReport) },
        GuardianMode::Exact,
    );
    enqueue_valid_session(&valid).await;
    let valid_receipt = valid.lifecycle.revoke_and_wait().await.unwrap();
    let valid_exact = valid_receipt.guardian.spawned_sessions == 1
        && valid_receipt.guardian.joined_sessions == 1
        && valid_receipt.guardian.joinset_empty
        && valid.available_permits() == 2;

    let panic = binding_fixture(
        BashHealth::ready(),
        async { Ok(ControlReport) },
        GuardianMode::Exact,
    );
    enqueue_signaled_fault(
        &panic,
        SessionFault::PanicWithSignal(Arc::new(tokio::sync::Notify::new())),
    )
    .await;
    let panic_rejected = matches!(
        panic.lifecycle.revoke_and_wait().await,
        Err(BindingDrainError::GuardianFailed)
    ) && panic.available_permits() == 2;

    let unclassified = binding_fixture(
        BashHealth::ready(),
        async { Ok(ControlReport) },
        GuardianMode::Exact,
    );
    enqueue_signaled_fault(
        &unclassified,
        SessionFault::UncategorizedWithSignal(Arc::new(tokio::sync::Notify::new())),
    )
    .await;
    let unclassified_rejected = matches!(
        unclassified.lifecycle.revoke_and_wait().await,
        Err(BindingDrainError::GuardianFailed)
    ) && unclassified.available_permits() == 2;
    GuardianTaskEvidence {
        valid_exact,
        panic_rejected,
        unclassified_rejected,
    }
}
