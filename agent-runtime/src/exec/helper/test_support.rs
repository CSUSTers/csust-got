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

    fn rlimit(&mut self, _spec: &ExecSpec) -> Result<(), ()> {
        self.result(ExecInitStage::Rlimit)
    }

    fn close_inherited_fds(&mut self) -> Result<(), ()> {
        self.result(ExecInitStage::CloseInheritedFds)
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
