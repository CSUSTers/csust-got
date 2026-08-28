use crate::{runtime_security::RuntimeFetchSecurity, workspace_budget::WorkspaceBudget};
use std::{
    collections::HashMap,
    fmt, io,
    path::Path,
    sync::{Arc, Mutex},
    time::Duration,
};

mod binding;
#[cfg(all(feature = "c7-test-support", target_os = "linux"))]
pub mod c7_test_support;
#[cfg(target_os = "linux")]
mod control;
#[cfg(target_os = "linux")]
mod guardian;
mod lifecycle;
mod output;
mod protocol;
mod registry;
#[cfg(target_os = "linux")]
mod response;
#[cfg(target_os = "linux")]
mod session;
#[cfg(target_os = "linux")]
mod socket;
#[cfg(target_os = "linux")]
mod terminal;

pub use binding::{CommandBindingLease, CommandLaunch};
pub use lifecycle::{
    BindingDrainError, BindingDrainReceipt, CommandBindingPhase, CommandLifecycleLease,
    ControlReaderOutcome, GuardianReceipt,
};
pub use output::{OutputCommitGuard, OutputCommitOutcome};
pub use protocol::{
    CommandControlPacket, LocalRequestState, LocalResponseState, MAX_COMMAND_CONTROL_PACKET_BYTES,
};

pub const LOCAL_SESSION_CHANNEL_CAPACITY: usize =
    crate::fetch_protocol::LOCAL_SESSION_CHANNEL_CAPACITY;
pub const COMMAND_CONTROL_CANCEL_GRACE: Duration = Duration::from_millis(100);
pub const MAX_COMMAND_CONTROL_PACKETS: usize = 20;
pub const MAX_ACTIVE_LOCAL_SESSIONS: usize = 2;
pub const SESSION_JOB_CHANNEL_CAPACITY: usize = 2;
const OWNER_DRAIN_TIMEOUT: Duration = Duration::from_secs(1);

#[derive(Debug)]
pub struct RuntimeFetchProxyError {
    category: RuntimeFetchProxyErrorCategory,
    message: String,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum RuntimeFetchProxyErrorCategory {
    Auth,
    Policy,
    Network,
    Timeout,
    Protocol,
    Internal,
}

impl RuntimeFetchProxyError {
    fn new(message: impl Into<String>) -> Self {
        Self {
            category: RuntimeFetchProxyErrorCategory::Internal,
            message: message.into(),
        }
    }

    fn protocol(message: String) -> Self {
        Self::with_category(RuntimeFetchProxyErrorCategory::Protocol, message)
    }

    fn policy(message: impl Into<String>) -> Self {
        Self::with_category(RuntimeFetchProxyErrorCategory::Policy, message)
    }

    #[cfg(target_os = "linux")]
    fn network(message: impl Into<String>) -> Self {
        Self::with_category(RuntimeFetchProxyErrorCategory::Network, message)
    }

    #[cfg(target_os = "linux")]
    fn timeout(message: impl Into<String>) -> Self {
        Self::with_category(RuntimeFetchProxyErrorCategory::Timeout, message)
    }

    fn with_category(category: RuntimeFetchProxyErrorCategory, message: impl Into<String>) -> Self {
        Self {
            category,
            message: message.into(),
        }
    }

    pub fn category(&self) -> RuntimeFetchProxyErrorCategory {
        self.category
    }
}

impl fmt::Display for RuntimeFetchProxyError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(&self.message)
    }
}

impl std::error::Error for RuntimeFetchProxyError {}

fn state_error(message: &'static str) -> RuntimeFetchProxyError {
    RuntimeFetchProxyError::protocol(message.to_string())
}

fn output_error(error: io::Error) -> RuntimeFetchProxyError {
    RuntimeFetchProxyError::new(format!("proxy output failed: {error}"))
}

#[derive(Clone)]
pub struct RuntimeFetchProxy {
    inner: Arc<ProxyInner>,
}

enum ProxyMode {
    Disabled,
    Enabled(RuntimeFetchSecurity),
}

#[cfg_attr(not(target_os = "linux"), allow(dead_code))]
struct ProxyInner {
    mode: ProxyMode,
    workspace_budget: WorkspaceBudget,
    registry: Mutex<HashMap<String, Arc<registry::BindingEntry>>>,
}

impl RuntimeFetchProxy {
    pub fn disabled(workspace_budget: WorkspaceBudget) -> Self {
        Self {
            inner: Arc::new(ProxyInner {
                mode: ProxyMode::Disabled,
                workspace_budget,
                registry: Mutex::new(HashMap::new()),
            }),
        }
    }

    pub fn enabled(security: RuntimeFetchSecurity, workspace_budget: WorkspaceBudget) -> Self {
        Self {
            inner: Arc::new(ProxyInner {
                mode: ProxyMode::Enabled(security),
                workspace_budget,
                registry: Mutex::new(HashMap::new()),
            }),
        }
    }

    pub fn is_enabled(&self) -> bool {
        matches!(self.inner.mode, ProxyMode::Enabled(_))
    }

    pub fn new_command_id(&self) -> Result<String, RuntimeFetchProxyError> {
        match &self.inner.mode {
            ProxyMode::Enabled(security) => security
                .new_command_id()
                .map_err(|error| RuntimeFetchProxyError::new(error.to_string())),
            ProxyMode::Disabled => {
                let mut bytes = [0_u8; 16];
                getrandom::fill(&mut bytes).map_err(|_| {
                    RuntimeFetchProxyError::new("runtime could not create a command identity")
                })?;
                Ok(bytes.iter().map(|byte| format!("{byte:02x}")).collect())
            }
        }
    }

    pub async fn probe(&self) -> bool {
        match &self.inner.mode {
            ProxyMode::Disabled => false,
            ProxyMode::Enabled(security) => security.probe().await,
        }
    }

    pub fn policy_version(&self) -> &str {
        match &self.inner.mode {
            ProxyMode::Disabled => "",
            ProxyMode::Enabled(security) => security.policy_version(),
        }
    }

    pub fn socket_path(&self) -> Option<&Path> {
        match &self.inner.mode {
            ProxyMode::Disabled => None,
            ProxyMode::Enabled(security) => Some(security.socket_path()),
        }
    }

    pub fn shell_environment(&self) -> Vec<(String, String)> {
        vec![
            (
                "PATH".to_string(),
                crate::runtime_security::SHELL_PATH.to_string(),
            ),
            ("HOME".to_string(), "/tmp".to_string()),
            (
                "AGENT_FETCH_CONTROL_FD".to_string(),
                crate::exec::COMMAND_CONTROL_FD.to_string(),
            ),
        ]
    }
}
