mod lifecycle;
mod output;
mod terminal;

pub(crate) use lifecycle::held_binding;
pub use lifecycle::{
    ControlPanicReceipt, DeferredBinding, GuardianMismatchReceipt, GuardianTimeoutReceipt,
    SharedHealthReceipt, control_panic_receipt, guardian_mismatch_receipt,
    guardian_timeout_receipt, shared_supervisor_health_receipt,
};
pub use output::{
    OutputTerminalReceipt, PostRenameReceipt, PreRenameReceipt, internal_terminal_receipt,
    policy_terminal_receipt, post_rename_receipt, pre_rename_receipt,
};

pub struct TerminalGuardianEvidence {
    pub broker_error_then_internal_exactly_once: bool,
    pub writer_unavailable_no_frame: bool,
    pub valid_guardian_exact: bool,
    pub panic_guardian_rejected: bool,
    pub unclassified_guardian_rejected: bool,
}

pub async fn terminal_guardian_evidence() -> TerminalGuardianEvidence {
    let writer = terminal::terminal_writer_evidence().await;
    let guardian = lifecycle::guardian_task_evidence().await;
    TerminalGuardianEvidence {
        broker_error_then_internal_exactly_once: writer.exactly_once,
        writer_unavailable_no_frame: writer.unavailable,
        valid_guardian_exact: guardian.valid_exact,
        panic_guardian_rejected: guardian.panic_rejected,
        unclassified_guardian_rejected: guardian.unclassified_rejected,
    }
}
