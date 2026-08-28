use std::fmt;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ControlReaderOutcome {
    Completed,
    Error,
    Panicked,
    Cancelled,
}

impl ControlReaderOutcome {
    #[cfg_attr(all(not(test), not(target_os = "linux")), allow(dead_code))]
    pub(crate) fn marker_label(self) -> &'static str {
        match self {
            Self::Completed => "completed",
            Self::Error => "error",
            Self::Panicked => "panicked",
            Self::Cancelled => "cancelled",
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct GuardianReceipt {
    pub spawned_sessions: usize,
    pub joined_sessions: usize,
    pub joinset_empty: bool,
    pub job_channel_closed: bool,
}

impl GuardianReceipt {
    pub(super) fn is_exact(self) -> bool {
        self.joinset_empty
            && self.job_channel_closed
            && self.spawned_sessions == self.joined_sessions
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct BindingDrainReceipt {
    pub control_reader: ControlReaderOutcome,
    pub guardian: GuardianReceipt,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum BindingDrainError {
    DrainPending,
    ControlReceiptMissing,
    GuardianReceiptMissing,
    GuardianFailed,
    ReceiptMismatch,
    StateUnavailable,
}

impl fmt::Display for BindingDrainError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(match self {
            Self::DrainPending => "command binding drain is pending",
            Self::ControlReceiptMissing => "command binding control receipt is missing",
            Self::GuardianReceiptMissing => "command binding guardian receipt is missing",
            Self::GuardianFailed => "command binding guardian failed",
            Self::ReceiptMismatch => "command binding guardian receipt is incomplete",
            Self::StateUnavailable => "command binding drain state is unavailable",
        })
    }
}

impl std::error::Error for BindingDrainError {}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum GuardianTaskOutcome {
    Success(GuardianReceipt),
    Error,
    Panicked,
    Cancelled,
}
