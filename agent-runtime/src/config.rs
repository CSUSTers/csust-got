use crate::{
    cgroup::{CgroupConfig, CgroupTopologyConfig, CommandLimits},
    exec::RlimitSpec,
    fetch_protocol::{FETCH_PROTOCOL_VERSION, SecretString},
};
use std::{fmt, net::SocketAddr, path::PathBuf, time::Duration};

mod parse;

pub(crate) use parse::{
    bounded_duration, bounded_number, load_signing_key, required_absolute_path, required_list,
    required_positive_number, required_string,
};
use parse::{
    bounded_nonnegative_number, optional_bool, optional_path, optional_string,
    optional_supplied_path,
};

const DEFAULT_WORKSPACE_ROOT: &str = "workspaces";
const DEFAULT_SKILLS_ROOT: &str = "skills";
const DEFAULT_TRACE_PATH: &str = "logs/runtime-traces.jsonl";
const DEFAULT_LISTEN_ADDR: &str = "0.0.0.0:8080";
const DEFAULT_MAX_OUTPUT_CHARS: usize = 12_000;

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct FetchClaimLimits {
    pub protocol_version: u16,
    pub policy_version: String,
    pub max_concurrency: u16,
    pub max_requests: u16,
    pub max_request_bytes: u64,
    pub max_response_bytes: u64,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum RuntimeFetchConfig {
    Disabled,
    Enabled {
        socket_path: PathBuf,
        hmac_key_file: PathBuf,
        limits: FetchClaimLimits,
        require_for_readiness: bool,
    },
}

impl RuntimeFetchConfig {
    pub fn is_enabled(&self) -> bool {
        matches!(self, Self::Enabled { .. })
    }

    pub fn load_signing_key(&self) -> Result<Option<Vec<u8>>, ConfigError> {
        match self {
            Self::Disabled => Ok(None),
            Self::Enabled { hmac_key_file, .. } => {
                load_signing_key(hmac_key_file, "runtime").map(Some)
            }
        }
    }
}

pub struct RuntimeConfig {
    pub listen_addr: SocketAddr,
    pub workspace_root: PathBuf,
    pub workspace_max_bytes: u64,
    pub skills_root: PathBuf,
    pub cgroup: CgroupConfig,
    pub cgroup_topology: CgroupTopologyConfig,
    pub rlimits: RlimitSpec,
    pub fetch: RuntimeFetchConfig,
    pub command_timeout: Duration,
    pub auth_token: SecretString,
    pub max_output_chars: usize,
    pub trace_jsonl_path: PathBuf,
    pub exec_helper: Option<PathBuf>,
}

impl RuntimeConfig {
    pub fn from_env(get: impl Fn(&str) -> Option<String>) -> Result<Self, ConfigError> {
        let defaults = CommandLimits::approved_defaults();
        let rlimit_defaults = RlimitSpec::approved_defaults();
        let listen_addr = optional_string(&get, "AGENT_RUNTIME_ADDR", DEFAULT_LISTEN_ADDR)?
            .parse()
            .map_err(|_| ConfigError::new("AGENT_RUNTIME_ADDR must be a socket address"))?;
        let workspace_root =
            optional_path(&get, "AGENT_RUNTIME_WORKSPACE_ROOT", DEFAULT_WORKSPACE_ROOT)?;
        let skills_root = optional_path(&get, "AGENT_RUNTIME_SKILLS_ROOT", DEFAULT_SKILLS_ROOT)?;
        let trace_jsonl_path =
            optional_path(&get, "AGENT_RUNTIME_TRACE_JSONL", DEFAULT_TRACE_PATH)?;
        let cgroup_root = required_absolute_path(&get, "AGENT_RUNTIME_CGROUP_ROOT")?;
        let aggregate_cgroup_root =
            required_absolute_path(&get, "AGENT_RUNTIME_CGROUP_AGGREGATE_ROOT")?;
        let workspace_max_bytes =
            required_positive_number(&get, "AGENT_RUNTIME_WORKSPACE_MAX_BYTES")?;
        let auth_token = SecretString::new(required_string(&get, "AGENT_RUNTIME_TOKEN")?);

        if let Some(mode) = get("AGENT_RUNTIME_BASH_SANDBOX")
            && mode.trim() != "proot"
        {
            return Err(ConfigError::new("AGENT_RUNTIME_BASH_SANDBOX must be proot"));
        }

        let command_timeout_secs = bounded_number(
            &get,
            "AGENT_RUNTIME_COMMAND_TIMEOUT_SECS",
            defaults.cpu_budget.as_secs(),
            defaults.cpu_budget.as_secs(),
        )?;
        let command_timeout = Duration::from_secs(command_timeout_secs);
        let cgroup = CgroupConfig {
            root: cgroup_root.clone(),
            limits: CommandLimits {
                pids_max: bounded_number(
                    &get,
                    "AGENT_RUNTIME_COMMAND_PIDS_MAX",
                    defaults.pids_max,
                    defaults.pids_max,
                )?,
                memory_max_bytes: bounded_number(
                    &get,
                    "AGENT_RUNTIME_COMMAND_MEMORY_MAX_BYTES",
                    defaults.memory_max_bytes,
                    defaults.memory_max_bytes,
                )?,
                memory_swap_max_bytes: bounded_nonnegative_number(
                    &get,
                    "AGENT_RUNTIME_COMMAND_MEMORY_SWAP_MAX_BYTES",
                    defaults.memory_swap_max_bytes,
                    defaults.memory_swap_max_bytes,
                )?,
                cpu_quota_us: bounded_number(
                    &get,
                    "AGENT_RUNTIME_COMMAND_CPU_QUOTA_US",
                    defaults.cpu_quota_us,
                    defaults.cpu_quota_us,
                )?,
                cpu_period_us: bounded_number(
                    &get,
                    "AGENT_RUNTIME_COMMAND_CPU_PERIOD_US",
                    defaults.cpu_period_us,
                    defaults.cpu_period_us,
                )?,
                cpu_budget: Duration::from_secs(bounded_number(
                    &get,
                    "AGENT_RUNTIME_COMMAND_CPU_BUDGET_SECS",
                    defaults.cpu_budget.as_secs(),
                    defaults.cpu_budget.as_secs(),
                )?),
                cleanup_timeout: defaults.cleanup_timeout,
            },
        };
        if cgroup.limits.cpu_quota_us > cgroup.limits.cpu_period_us {
            return Err(ConfigError::new(
                "AGENT_RUNTIME_COMMAND_CPU_QUOTA_US must not exceed AGENT_RUNTIME_COMMAND_CPU_PERIOD_US",
            ));
        }
        let rlimits = RlimitSpec {
            nproc: bounded_number(
                &get,
                "AGENT_RUNTIME_COMMAND_NPROC",
                rlimit_defaults.nproc,
                rlimit_defaults.nproc,
            )?,
            nofile: bounded_number(
                &get,
                "AGENT_RUNTIME_COMMAND_NOFILE",
                rlimit_defaults.nofile,
                rlimit_defaults.nofile,
            )?,
            fsize_bytes: bounded_number(
                &get,
                "AGENT_RUNTIME_COMMAND_FSIZE_BYTES",
                rlimit_defaults.fsize_bytes,
                rlimit_defaults.fsize_bytes,
            )?,
            core_bytes: 0,
        };
        let fetch = if optional_bool(&get, "AGENT_RUNTIME_FETCH_ENABLED", false)? {
            let limits = FetchClaimLimits {
                protocol_version: FETCH_PROTOCOL_VERSION,
                policy_version: required_string(&get, "AGENT_FETCH_POLICY_VERSION")?,
                max_concurrency: bounded_number(&get, "AGENT_RUNTIME_FETCH_MAX_CONCURRENCY", 2, 2)?,
                max_requests: bounded_number(&get, "AGENT_RUNTIME_FETCH_MAX_REQUESTS", 20, 20)?,
                max_request_bytes: bounded_number(
                    &get,
                    "AGENT_RUNTIME_FETCH_MAX_REQUEST_BYTES",
                    8 * 1024 * 1024,
                    8 * 1024 * 1024,
                )?,
                max_response_bytes: bounded_number(
                    &get,
                    "AGENT_RUNTIME_FETCH_MAX_RESPONSE_BYTES",
                    32 * 1024 * 1024,
                    32 * 1024 * 1024,
                )?,
            };
            RuntimeFetchConfig::Enabled {
                socket_path: required_absolute_path(&get, "AGENT_FETCH_SOCKET")?,
                hmac_key_file: required_absolute_path(&get, "AGENT_FETCH_HMAC_KEY_FILE")?,
                limits,
                require_for_readiness: optional_bool(
                    &get,
                    "AGENT_RUNTIME_REQUIRE_FETCH_FOR_READINESS",
                    true,
                )?,
            }
        } else {
            RuntimeFetchConfig::Disabled
        };

        Ok(Self {
            listen_addr,
            workspace_root,
            workspace_max_bytes,
            skills_root,
            cgroup,
            cgroup_topology: CgroupTopologyConfig {
                aggregate_root: aggregate_cgroup_root,
                commands_root: cgroup_root,
            },
            rlimits,
            fetch,
            command_timeout,
            auth_token,
            max_output_chars: bounded_number(
                &get,
                "AGENT_RUNTIME_MAX_OUTPUT_CHARS",
                DEFAULT_MAX_OUTPUT_CHARS,
                usize::MAX,
            )?,
            trace_jsonl_path,
            exec_helper: optional_supplied_path(&get, "AGENT_RUNTIME_EXEC_HELPER")?,
        })
    }
}

impl fmt::Debug for RuntimeConfig {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter
            .debug_struct("RuntimeConfig")
            .field("listen_addr", &self.listen_addr)
            .field("workspace_root", &self.workspace_root)
            .field("workspace_max_bytes", &self.workspace_max_bytes)
            .field("skills_root", &self.skills_root)
            .field("cgroup", &self.cgroup)
            .field("cgroup_topology", &self.cgroup_topology)
            .field("rlimits", &self.rlimits)
            .field("fetch", &self.fetch)
            .field("command_timeout", &self.command_timeout)
            .field("auth_token", &"[REDACTED]")
            .field("max_output_chars", &self.max_output_chars)
            .field("trace_jsonl_path", &self.trace_jsonl_path)
            .field("exec_helper", &self.exec_helper)
            .finish()
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ConfigError {
    message: String,
}

impl ConfigError {
    pub(crate) fn new(message: impl Into<String>) -> Self {
        Self {
            message: message.into(),
        }
    }
}

impl fmt::Display for ConfigError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(&self.message)
    }
}

impl std::error::Error for ConfigError {}
