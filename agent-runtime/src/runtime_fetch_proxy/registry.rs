use super::{
    ProxyInner,
    lifecycle::{CommandBindingPhase, ControlReaderOutcome, GuardianReceipt, GuardianTaskOutcome},
};
use crate::{
    exec::BashHealth, runtime_security::IssuedFetchCommand, workspace_budget::WorkspaceBudget,
};
use std::{
    path::PathBuf,
    sync::{Arc, Mutex, Weak},
};
use tokio::{sync::Mutex as AsyncMutex, task::JoinHandle};
use tokio_util::sync::CancellationToken;

pub(super) struct BindingEntry {
    pub(super) command_id: String,
    pub(super) context: Arc<BindingContext>,
    pub(super) control_cancel: CancellationToken,
    pub(super) guardian_cancel: CancellationToken,
    pub(super) control_reader:
        AsyncMutex<Option<JoinHandle<Result<ControlReport, super::RuntimeFetchProxyError>>>>,
    pub(super) control_outcome: AsyncMutex<Option<ControlReaderOutcome>>,
    pub(super) guardian:
        AsyncMutex<Option<JoinHandle<Result<GuardianReport, super::RuntimeFetchProxyError>>>>,
    pub(super) guardian_outcome: AsyncMutex<Option<GuardianTaskOutcome>>,
    #[cfg(target_os = "linux")]
    pub(super) job_sender: Mutex<Option<tokio::sync::mpsc::Sender<super::control::SessionJob>>>,
    pub(super) proxy: Weak<ProxyInner>,
}

#[cfg_attr(not(target_os = "linux"), allow(dead_code))]
pub(super) struct BindingContext {
    pub(super) phase: Arc<Mutex<CommandBindingPhase>>,
    pub(super) namespace: String,
    pub(super) workspace_budget: WorkspaceBudget,
    pub(super) health: BashHealth,
    pub(super) issued: Option<IssuedFetchCommand>,
    pub(super) broker_socket: Option<PathBuf>,
    #[cfg(target_os = "linux")]
    pub(super) session_permits: Arc<tokio::sync::Semaphore>,
}

#[derive(Default)]
pub(super) struct ControlReport;

pub(super) type GuardianReport = GuardianReceipt;
