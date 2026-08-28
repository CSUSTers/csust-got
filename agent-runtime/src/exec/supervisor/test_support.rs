use super::*;

impl CommandSupervisor {
    #[cfg(all(feature = "c7-test-support", target_os = "linux"))]
    pub(in crate::exec) fn test_production_with_spawn_controls(
        cgroups: CgroupManager,
        exec_helper: PathBuf,
        spawn_controls: SpawnControls,
    ) -> Self {
        Self {
            inner: Arc::new(SupervisorInner {
                backend: SupervisorBackend::Production {
                    cgroups,
                    exec_helper,
                    rlimits: RlimitSpec::approved_defaults(),
                    spawn_controls,
                },
                sequence: AtomicU64::new(0),
                health: BashHealth::ready(),
                deferred: DeferredCleanupRegistry::default(),
            }),
        }
    }

    pub fn test_direct() -> Self {
        Self::test_direct_with_cpu_budget(None)
    }

    pub(in crate::exec) fn test_direct_with_cpu_budget(cpu_budget: Option<Duration>) -> Self {
        Self::test_direct_with_faults(cpu_budget, DirectSupervisorFaults::default())
    }

    #[cfg(test)]
    pub(in crate::exec) fn test_direct_with_exec_failure_stage(
        stage: ExecInitFailureStage,
    ) -> Self {
        Self::test_direct_with_faults(
            None,
            DirectSupervisorFaults {
                exec_status: Some(DirectExecStatus::Failure(stage)),
                ..DirectSupervisorFaults::default()
            },
        )
    }

    #[cfg(test)]
    pub(in crate::exec) fn test_direct_with_exec_status_payload(payload: Vec<u8>) -> Self {
        Self::test_direct_with_faults(
            None,
            DirectSupervisorFaults {
                exec_status: Some(DirectExecStatus::Payload(payload)),
                ..DirectSupervisorFaults::default()
            },
        )
    }

    #[cfg(test)]
    pub(in crate::exec) fn test_direct_with_exec_status_timeout() -> Self {
        Self::test_direct_with_faults(
            None,
            DirectSupervisorFaults {
                exec_status: Some(DirectExecStatus::Pending),
                ..DirectSupervisorFaults::default()
            },
        )
    }

    #[cfg(test)]
    pub(in crate::exec) fn test_direct_with_exec_status_read_error() -> Self {
        Self::test_direct_with_faults(
            None,
            DirectSupervisorFaults {
                exec_status: Some(DirectExecStatus::ReadError),
                ..DirectSupervisorFaults::default()
            },
        )
    }

    #[cfg(test)]
    pub(in crate::exec) fn test_direct_with_helper_launch_failure(
        fault: HelperLaunchFault,
    ) -> Self {
        Self::test_direct_with_faults(
            None,
            DirectSupervisorFaults {
                helper_launch: Some(fault),
                ..DirectSupervisorFaults::default()
            },
        )
    }

    #[cfg(test)]
    pub(in crate::exec) fn test_direct_with_cgroup_create_failure() -> Self {
        Self::test_direct_with_faults(
            None,
            DirectSupervisorFaults {
                cgroup_create: true,
                ..DirectSupervisorFaults::default()
            },
        )
    }

    #[cfg(test)]
    pub(in crate::exec) fn test_direct_with_cpu_read_failure() -> Self {
        Self::test_direct_with_faults(
            Some(Duration::from_millis(20)),
            DirectSupervisorFaults {
                cpu_read: true,
                ..DirectSupervisorFaults::default()
            },
        )
    }

    pub(in crate::exec) fn test_direct_with_trace(
        cgroup_cleanup: bool,
    ) -> (Self, Arc<Mutex<Vec<&'static str>>>) {
        let trace = Arc::new(Mutex::new(Vec::new()));
        let supervisor = Self::test_direct_with_faults(
            None,
            DirectSupervisorFaults {
                trace: Some(Arc::clone(&trace)),
                cgroup_cleanup,
                ..DirectSupervisorFaults::default()
            },
        );
        (supervisor, trace)
    }

    pub(in crate::exec) fn test_direct_with_cleanup_probe()
    -> (Self, Arc<Mutex<Vec<&'static str>>>, Arc<CleanupProbe>) {
        let trace = Arc::new(Mutex::new(Vec::new()));
        let cleanup_probe = Arc::new(CleanupProbe::default());
        let supervisor = Self::test_direct_with_faults(
            None,
            DirectSupervisorFaults {
                trace: Some(Arc::clone(&trace)),
                cleanup_probe: Some(Arc::clone(&cleanup_probe)),
                ..DirectSupervisorFaults::default()
            },
        );
        (supervisor, trace, cleanup_probe)
    }

    fn test_direct_with_faults(
        cpu_budget: Option<Duration>,
        faults: DirectSupervisorFaults,
    ) -> Self {
        Self {
            inner: Arc::new(SupervisorInner {
                backend: SupervisorBackend::Direct { cpu_budget, faults },
                sequence: AtomicU64::new(0),
                health: BashHealth::ready(),
                deferred: DeferredCleanupRegistry::default(),
            }),
        }
    }
}
