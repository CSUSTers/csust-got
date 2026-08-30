#[cfg(target_os = "linux")]
use crate::cgroup::CgroupManager;
#[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
use crate::cgroup::CommandCgroup;
use crate::cgroup::{CgroupConfig, CgroupError, CommandIdentity};
use crate::runtime_fetch_proxy::CommandLaunch;
#[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
use crate::runtime_fetch_proxy::CommandLifecycleLease;
#[cfg(target_os = "linux")]
use crate::sandbox;
use serde::{Deserialize, Serialize};
#[cfg(target_os = "linux")]
use std::io::Write as _;
#[cfg(test)]
use std::pin::Pin;
#[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
use std::process::Stdio;
#[cfg(any(test, feature = "c7-test-support"))]
use std::sync::Mutex;
#[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
use std::{collections::BTreeSet, io, process::ExitStatus};
use std::{
    fmt,
    path::{Path, PathBuf},
    sync::{
        Arc,
        atomic::{AtomicU64, Ordering},
    },
    time::{Duration, SystemTime, UNIX_EPOCH},
};
#[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
use tokio::io::AsyncRead;
#[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
use tokio::io::AsyncReadExt;
#[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
use tokio::process::Command;
use tokio::{process::Child, sync::watch, task::JoinHandle};
#[cfg(target_os = "linux")]
use zeroize::Zeroize;

#[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
mod deferred;
mod health;
#[cfg(target_os = "linux")]
mod helper;
mod launch;
mod model;
mod spawn;
mod status;
mod status_reader;
#[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
mod supervision;
mod supervisor;

pub use health::BashHealth;
#[cfg(target_os = "linux")]
pub use helper::exec_from_config_fd;
pub use model::{CommandOutput, ExecError, ExecSpec, ExecTarget, RlimitSpec, SupervisorError};
#[cfg(test)]
use spawn::helper_argv;
#[cfg(target_os = "linux")]
pub use spawn::install_exec_fds;
#[cfg(any(test, feature = "c7-test-support"))]
use spawn::spawn_direct;
#[cfg(all(target_os = "linux", not(feature = "c7-test-support")))]
use spawn::spawn_exec_helper_with_control;
#[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
use spawn::validate_environment;
#[cfg(all(feature = "c7-test-support", target_os = "linux"))]
use spawn::{SpawnControls, spawn_exec_helper_with_control_and_controls};
pub use spawn::{SpawnedExecHelper, spawn_exec_helper};
#[cfg(test)]
use status::read_exec_startup_for_test;
#[cfg(target_os = "linux")]
pub use status::write_exec_status_failure;
pub use status::{
    EXEC_STARTUP_TIMEOUT, EXEC_STATUS_RECORD_BYTES, ExecInitFailureStage, ExecInitStage,
    ExecStartupChannelError, ExecStartupOutcome, ExecStatusError, ExecStatusOutcome,
    ExecStatusRecord, await_exec_status, encode_exec_status_failure,
};
use status_reader::ExecStartupStatusReader;
#[cfg(test)]
use supervisor::HelperLaunchFault;
pub use supervisor::{CommandHandle, CommandSupervisor};

#[cfg(target_os = "linux")]
const MAX_EXEC_SPEC_BYTES: usize = 32 * 1024;
#[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
const OUTPUT_READ_BUFFER_SIZE: usize = 8 * 1024;
#[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
const TRUNCATION_MARKER: &str = "\n[truncated]";
#[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
const CPU_POLL_INTERVAL: Duration = Duration::from_millis(50);
pub const EXEC_CONFIG_FD: i32 = 3;
pub const COMMAND_CONTROL_FD: i32 = 4;
pub const EXEC_STATUS_FD: i32 = 5;
pub const EXEC_CONFIG_FLAG: &str = "--config-fd";
#[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
const ALLOWED_ENVIRONMENT: [&str; 3] = ["PATH", "HOME", "AGENT_FETCH_CONTROL_FD"];

#[cfg(all(feature = "c7-test-support", target_os = "linux"))]
pub(crate) mod c7_test_support;

#[cfg(test)]
mod tests;
