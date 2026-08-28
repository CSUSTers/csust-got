#[cfg(any(test, feature = "c7-test-support"))]
use super::deferred::CleanupProbe;
#[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
use super::deferred::DeferredCleanupRegistry;
use super::*;

#[cfg(any(test, feature = "c7-test-support"))]
mod test_support;

#[derive(Clone)]
pub struct CommandSupervisor {
    pub(super) inner: Arc<SupervisorInner>,
}

pub(super) struct SupervisorInner {
    #[cfg_attr(all(not(test), not(target_os = "linux")), allow(dead_code))]
    pub(super) backend: SupervisorBackend,
    pub(super) sequence: AtomicU64,
    pub(super) health: BashHealth,
    #[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
    pub(super) deferred: DeferredCleanupRegistry,
}

pub(super) enum SupervisorBackend {
    #[cfg(target_os = "linux")]
    Production {
        cgroups: CgroupManager,
        exec_helper: PathBuf,
        rlimits: RlimitSpec,
        #[cfg(feature = "c7-test-support")]
        spawn_controls: SpawnControls,
    },
    #[cfg(any(test, feature = "c7-test-support"))]
    Direct {
        cpu_budget: Option<Duration>,
        faults: DirectSupervisorFaults,
    },
    #[cfg(all(not(target_os = "linux"), not(test), not(feature = "c7-test-support")))]
    #[allow(dead_code)]
    Unsupported,
}

#[cfg(any(test, feature = "c7-test-support"))]
#[derive(Clone, Default)]
pub(super) struct DirectSupervisorFaults {
    pub(super) cgroup_create: bool,
    pub(super) cpu_read: bool,
    #[cfg(test)]
    pub(super) exec_status: Option<DirectExecStatus>,
    pub(super) trace: Option<Arc<Mutex<Vec<&'static str>>>>,
    pub(super) cgroup_cleanup: bool,
    pub(super) cleanup_probe: Option<Arc<CleanupProbe>>,
    #[cfg(test)]
    pub(super) helper_launch: Option<HelperLaunchFault>,
}

#[cfg(test)]
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) enum HelperLaunchFault {
    BinarySpawn,
    PreExec,
    ConfigWrite,
    ConfigWriterPanic,
}

#[cfg(test)]
impl HelperLaunchFault {
    pub(super) const ALL: [Self; 4] = [
        Self::BinarySpawn,
        Self::PreExec,
        Self::ConfigWrite,
        Self::ConfigWriterPanic,
    ];
}

#[cfg(test)]
#[derive(Clone)]
pub(super) enum DirectExecStatus {
    Failure(ExecInitFailureStage),
    Payload(Vec<u8>),
    Pending,
    ReadError,
}

#[cfg(test)]
pub(super) struct PendingExecStatus;

#[cfg(test)]
pub(super) struct ErrorExecStatus;

#[cfg(test)]
impl AsyncRead for PendingExecStatus {
    fn poll_read(
        self: Pin<&mut Self>,
        _context: &mut std::task::Context<'_>,
        _buffer: &mut tokio::io::ReadBuf<'_>,
    ) -> std::task::Poll<io::Result<()>> {
        std::task::Poll::Pending
    }
}

#[cfg(test)]
impl AsyncRead for ErrorExecStatus {
    fn poll_read(
        self: Pin<&mut Self>,
        _context: &mut std::task::Context<'_>,
        _buffer: &mut tokio::io::ReadBuf<'_>,
    ) -> std::task::Poll<io::Result<()>> {
        std::task::Poll::Ready(Err(io::Error::other("injected status read failure")))
    }
}

pub struct CommandHandle {
    pub(super) cancel: Option<watch::Sender<bool>>,
    pub(super) result: Option<JoinHandle<Result<CommandOutput, SupervisorError>>>,
}

impl CommandSupervisor {
    pub fn production(config: CgroupConfig, exec_helper: PathBuf) -> Result<Self, SupervisorError> {
        Self::production_with_rlimits(config, RlimitSpec::approved_defaults(), exec_helper)
    }

    pub fn production_with_rlimits(
        config: CgroupConfig,
        rlimits: RlimitSpec,
        exec_helper: PathBuf,
    ) -> Result<Self, SupervisorError> {
        #[cfg(not(target_os = "linux"))]
        {
            let _ = (config, rlimits, exec_helper);
            return Err(SupervisorError::Unavailable(
                "agent runtime production execution requires Linux".to_string(),
            ));
        }
        #[cfg(target_os = "linux")]
        {
            let metadata = std::fs::metadata(&exec_helper).map_err(|error| {
                SupervisorError::Unavailable(format!(
                    "exec helper {} is unavailable: {error}",
                    exec_helper.display()
                ))
            })?;
            if !metadata.is_file() {
                return Err(SupervisorError::Unavailable(format!(
                    "exec helper {} is not a file",
                    exec_helper.display()
                )));
            }
            let cgroups = CgroupManager::validate(config.root, config.limits)?;
            Ok(Self {
                inner: Arc::new(SupervisorInner {
                    backend: SupervisorBackend::Production {
                        cgroups,
                        exec_helper,
                        rlimits,
                        #[cfg(feature = "c7-test-support")]
                        spawn_controls: SpawnControls::default(),
                    },
                    sequence: AtomicU64::new(0),
                    health: BashHealth::ready(),
                    deferred: DeferredCleanupRegistry::default(),
                }),
            })
        }
    }

    pub fn health(&self) -> BashHealth {
        self.inner.health.clone()
    }

    pub async fn shutdown(&self) -> Result<(), SupervisorError> {
        self.inner.health.begin_shutdown();
        self.inner.health.wait_for_drain().await
    }

    pub async fn recover_stale(&self) -> Result<(), SupervisorError> {
        #[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
        {
            match &self.inner.backend {
                #[cfg(target_os = "linux")]
                SupervisorBackend::Production { cgroups, .. } => {
                    cgroups.recover_stale().map_err(SupervisorError::from)?;
                }
                #[cfg(any(test, feature = "c7-test-support"))]
                SupervisorBackend::Direct { faults, .. } => {
                    if let Some(probe) = &faults.cleanup_probe {
                        probe.record_stale_recovery();
                    }
                }
            }
            return self
                .inner
                .deferred
                .recover_all()
                .await
                .map_err(SupervisorError::Cleanup);
        }
        #[cfg(all(not(target_os = "linux"), not(test), not(feature = "c7-test-support")))]
        {
            Err(SupervisorError::Unavailable(
                "agent runtime production execution requires Linux".to_string(),
            ))
        }
    }

    #[cfg(any(test, feature = "c7-test-support"))]
    pub(super) async fn deferred_cleanup_count_for_tests(&self) -> usize {
        self.inner.deferred.len()
    }
}
