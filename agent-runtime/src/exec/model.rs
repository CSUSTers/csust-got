use super::*;

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct RlimitSpec {
    pub nproc: u64,
    pub nofile: u64,
    pub fsize_bytes: u64,
    pub core_bytes: u64,
}

impl RlimitSpec {
    pub fn approved_defaults() -> Self {
        Self {
            nproc: 480,
            nofile: 256,
            fsize_bytes: 64 * 1024 * 1024,
            core_bytes: 0,
        }
    }
}

impl Default for RlimitSpec {
    fn default() -> Self {
        Self::approved_defaults()
    }
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct ExecSpec {
    pub cgroup_procs: PathBuf,
    pub program: PathBuf,
    pub args: Vec<String>,
    pub cwd: PathBuf,
    pub env: Vec<(String, String)>,
    pub rlimits: RlimitSpec,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ExecTarget {
    pub program: PathBuf,
    pub args: Vec<String>,
    pub cwd: PathBuf,
}

#[derive(Debug)]
pub struct CommandOutput {
    pub exit_code: i32,
    pub stdout: String,
    pub stderr: String,
    pub truncated: bool,
    pub cgroup_name: String,
}

#[derive(Debug)]
pub struct ExecError {
    message: String,
}

impl ExecError {
    pub(super) fn new(message: impl Into<String>) -> Self {
        Self {
            message: message.into(),
        }
    }
}

impl fmt::Display for ExecError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(&self.message)
    }
}

impl std::error::Error for ExecError {}

#[derive(Debug)]
pub enum SupervisorError {
    Unavailable(String),
    Spawn(String),
    Command(String),
    TimedOut,
    Canceled,
    CpuBudgetExceeded,
    Cleanup(String),
    CleanupDeferred(String),
}

impl SupervisorError {
    pub fn is_timeout(&self) -> bool {
        matches!(self, Self::TimedOut)
    }

    pub fn is_canceled(&self) -> bool {
        matches!(self, Self::Canceled)
    }

    pub fn is_unavailable(&self) -> bool {
        matches!(self, Self::Unavailable(_) | Self::CleanupDeferred(_))
    }

    pub fn preserves_cleanup_state(&self) -> bool {
        matches!(self, Self::CleanupDeferred(_))
    }
}

impl fmt::Display for SupervisorError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Unavailable(message)
            | Self::Spawn(message)
            | Self::Command(message)
            | Self::Cleanup(message)
            | Self::CleanupDeferred(message) => formatter.write_str(message),
            Self::TimedOut => formatter.write_str("command timed out"),
            Self::Canceled => formatter.write_str("command canceled"),
            Self::CpuBudgetExceeded => formatter.write_str("command CPU budget exceeded"),
        }
    }
}

impl std::error::Error for SupervisorError {}

impl From<CgroupError> for SupervisorError {
    fn from(error: CgroupError) -> Self {
        Self::Unavailable(error.to_string())
    }
}
