use super::*;

pub(in crate::exec) fn injected_failure_record(
    stage: ExecInitStage,
) -> [u8; EXEC_STATUS_RECORD_BYTES] {
    let mut ops = FaultExecInitOps { failure: stage };
    let observed = run_exec_init(&mut ops).unwrap_err();
    ExecStatusRecord { stage: observed }.encode()
}

struct FaultExecInitOps {
    failure: ExecInitStage,
}

impl FaultExecInitOps {
    fn result(&self, stage: ExecInitStage) -> Result<(), ()> {
        if self.failure == stage {
            Err(())
        } else {
            Ok(())
        }
    }

    fn spec() -> ExecSpec {
        ExecSpec {
            cgroup_procs: PathBuf::from("/test/cgroup.procs"),
            program: PathBuf::from("/test/target"),
            args: Vec::new(),
            cwd: PathBuf::from("/workspace"),
            env: Vec::new(),
            rlimits: RlimitSpec::approved_defaults(),
        }
    }
}

impl ExecInitOps for FaultExecInitOps {
    fn status_cloexec(&mut self) -> Result<(), ()> {
        self.result(ExecInitStage::StatusCloexec)
    }

    fn config_read(&mut self) -> Result<Vec<u8>, ()> {
        self.result(ExecInitStage::ConfigRead)?;
        Ok(b"{}".to_vec())
    }

    fn config_decode(&mut self, _payload: &[u8]) -> Result<ExecSpec, ()> {
        self.result(ExecInitStage::ConfigDecode)?;
        Ok(Self::spec())
    }

    fn config_close(&mut self) -> Result<(), ()> {
        self.result(ExecInitStage::ConfigClose)
    }

    fn cgroup_join(&mut self, _spec: &ExecSpec) -> Result<(), ()> {
        self.result(ExecInitStage::CgroupJoin)
    }

    fn capture_hard_nofile(&mut self) -> Result<libc::rlim_t, ()> {
        self.result(ExecInitStage::CloseInheritedFds)?;
        Ok(4_096)
    }

    fn close_inherited_fds(&mut self, _hard_nofile: libc::rlim_t) -> Result<(), ()> {
        self.result(ExecInitStage::CloseInheritedFds)
    }

    fn rlimit(&mut self, _spec: &ExecSpec) -> Result<(), ()> {
        self.result(ExecInitStage::Rlimit)
    }

    fn no_new_privs(&mut self) -> Result<(), ()> {
        self.result(ExecInitStage::NoNewPrivs)
    }

    fn seccomp(&mut self) -> Result<(), ()> {
        self.result(ExecInitStage::Seccomp)
    }

    fn target_exec(&mut self, _spec: ExecSpec) -> Result<std::convert::Infallible, ()> {
        Err(())
    }
}

#[test]
fn exec_initialization_captures_and_closes_fds_before_applying_rlimits() {
    struct OrderedExecInitOps {
        events: Vec<&'static str>,
        close_hard_nofile: Option<libc::rlim_t>,
    }

    impl OrderedExecInitOps {
        fn event(&mut self, event: &'static str) {
            self.events.push(event);
        }
    }

    impl ExecInitOps for OrderedExecInitOps {
        fn status_cloexec(&mut self) -> Result<(), ()> {
            self.event("status_cloexec");
            Ok(())
        }

        fn config_read(&mut self) -> Result<Vec<u8>, ()> {
            self.event("config_read");
            Ok(b"{}".to_vec())
        }

        fn config_decode(&mut self, _payload: &[u8]) -> Result<ExecSpec, ()> {
            self.event("config_decode");
            Ok(FaultExecInitOps::spec())
        }

        fn config_close(&mut self) -> Result<(), ()> {
            self.event("config_close");
            Ok(())
        }

        fn cgroup_join(&mut self, _spec: &ExecSpec) -> Result<(), ()> {
            self.event("cgroup_join");
            Ok(())
        }

        fn capture_hard_nofile(&mut self) -> Result<libc::rlim_t, ()> {
            self.event("capture_hard_nofile");
            Ok(4_096)
        }

        fn close_inherited_fds(&mut self, hard_nofile: libc::rlim_t) -> Result<(), ()> {
            self.event("close_inherited_fds");
            self.close_hard_nofile = Some(hard_nofile);
            Ok(())
        }

        fn rlimit(&mut self, _spec: &ExecSpec) -> Result<(), ()> {
            self.event("rlimit");
            Ok(())
        }

        fn no_new_privs(&mut self) -> Result<(), ()> {
            self.event("no_new_privs");
            Ok(())
        }

        fn seccomp(&mut self) -> Result<(), ()> {
            self.event("seccomp");
            Ok(())
        }

        fn target_exec(&mut self, _spec: ExecSpec) -> Result<std::convert::Infallible, ()> {
            self.event("target_exec");
            Err(())
        }
    }

    let mut ops = OrderedExecInitOps {
        events: Vec::new(),
        close_hard_nofile: None,
    };
    assert_eq!(run_exec_init(&mut ops), Err(ExecInitStage::TargetExec));
    assert_eq!(ops.close_hard_nofile, Some(4_096));
    assert_eq!(
        ops.events,
        [
            "status_cloexec",
            "config_read",
            "config_decode",
            "config_close",
            "cgroup_join",
            "capture_hard_nofile",
            "close_inherited_fds",
            "rlimit",
            "no_new_privs",
            "seccomp",
            "target_exec",
        ]
    );
}
