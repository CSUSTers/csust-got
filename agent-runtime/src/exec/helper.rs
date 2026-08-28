use super::*;
use std::os::unix::process::CommandExt;

#[cfg(test)]
pub(super) mod test_support;

trait ExecInitOps {
    fn status_cloexec(&mut self) -> Result<(), ()>;
    fn config_read(&mut self) -> Result<Vec<u8>, ()>;
    fn config_decode(&mut self, payload: &[u8]) -> Result<ExecSpec, ()>;
    fn config_close(&mut self) -> Result<(), ()>;
    fn cgroup_join(&mut self, spec: &ExecSpec) -> Result<(), ()>;
    fn rlimit(&mut self, spec: &ExecSpec) -> Result<(), ()>;
    fn close_inherited_fds(&mut self) -> Result<(), ()>;
    fn no_new_privs(&mut self) -> Result<(), ()>;
    fn seccomp(&mut self) -> Result<(), ()>;
    fn target_exec(&mut self, spec: ExecSpec) -> Result<std::convert::Infallible, ()>;
}

pub fn exec_from_config_fd(fd: std::os::fd::RawFd) -> anyhow::Result<std::convert::Infallible> {
    let mut ops = RealExecInitOps { config_fd: fd };
    match run_exec_init(&mut ops) {
        Ok(never) => match never {},
        Err(stage) => report_exec_init_failure(stage),
    }
}

fn run_exec_init<O: ExecInitOps>(ops: &mut O) -> Result<std::convert::Infallible, ExecInitStage> {
    ops.status_cloexec()
        .map_err(|()| ExecInitStage::StatusCloexec)?;
    let mut payload = ops.config_read().map_err(|()| ExecInitStage::ConfigRead)?;
    let spec = match ops.config_decode(&payload) {
        Ok(spec) => spec,
        Err(()) => {
            payload.zeroize();
            return Err(ExecInitStage::ConfigDecode);
        }
    };
    payload.zeroize();
    ops.config_close()
        .map_err(|()| ExecInitStage::ConfigClose)?;
    ops.cgroup_join(&spec)
        .map_err(|()| ExecInitStage::CgroupJoin)?;
    ops.rlimit(&spec).map_err(|()| ExecInitStage::Rlimit)?;
    ops.close_inherited_fds()
        .map_err(|()| ExecInitStage::CloseInheritedFds)?;
    ops.no_new_privs().map_err(|()| ExecInitStage::NoNewPrivs)?;
    ops.seccomp().map_err(|()| ExecInitStage::Seccomp)?;
    ops.target_exec(spec)
        .map_err(|()| ExecInitStage::TargetExec)
}

struct RealExecInitOps {
    config_fd: std::os::fd::RawFd,
}

impl ExecInitOps for RealExecInitOps {
    fn status_cloexec(&mut self) -> Result<(), ()> {
        set_descriptor_cloexec(EXEC_STATUS_FD).map_err(|_| ())
    }

    fn config_read(&mut self) -> Result<Vec<u8>, ()> {
        read_bounded_exec_spec(self.config_fd, MAX_EXEC_SPEC_BYTES).map_err(|_| ())
    }

    fn config_decode(&mut self, payload: &[u8]) -> Result<ExecSpec, ()> {
        serde_json::from_slice(payload).map_err(|_| ())
    }

    fn config_close(&mut self) -> Result<(), ()> {
        if unsafe { libc::close(self.config_fd) } == 0 {
            Ok(())
        } else {
            Err(())
        }
    }

    fn cgroup_join(&mut self, spec: &ExecSpec) -> Result<(), ()> {
        join_cgroup(&spec.cgroup_procs).map_err(|_| ())
    }

    fn rlimit(&mut self, spec: &ExecSpec) -> Result<(), ()> {
        apply_rlimits(&spec.rlimits).map_err(|_| ())
    }

    fn close_inherited_fds(&mut self) -> Result<(), ()> {
        let hard_nofile = sandbox::capture_hard_nofile().map_err(|_| ())?;
        sandbox::close_inherited_fds_except(hard_nofile, &[COMMAND_CONTROL_FD, EXEC_STATUS_FD])
            .map_err(|_| ())
    }

    fn no_new_privs(&mut self) -> Result<(), ()> {
        sandbox::set_no_new_privs().map_err(|_| ())
    }

    fn seccomp(&mut self) -> Result<(), ()> {
        sandbox::apply_untrusted_seccomp().map_err(|_| ())
    }

    fn target_exec(&mut self, spec: ExecSpec) -> Result<std::convert::Infallible, ()> {
        exec_target(spec).map_err(|_| ())
    }
}

fn set_descriptor_cloexec(fd: i32) -> io::Result<()> {
    let flags = unsafe { libc::fcntl(fd, libc::F_GETFD) };
    if flags == -1 || unsafe { libc::fcntl(fd, libc::F_SETFD, flags | libc::FD_CLOEXEC) } == -1 {
        return Err(io::Error::last_os_error());
    }
    let verified = unsafe { libc::fcntl(fd, libc::F_GETFD) };
    if verified == -1 {
        return Err(io::Error::last_os_error());
    }
    if verified & libc::FD_CLOEXEC == 0 {
        return Err(io::Error::other("status descriptor is not CLOEXEC"));
    }
    Ok(())
}

fn report_exec_init_failure<T>(stage: ExecInitStage) -> anyhow::Result<T> {
    let _ = write_exec_status_failure(EXEC_STATUS_FD, stage);
    Err(anyhow::anyhow!(
        "exec helper enforcement initialization failed"
    ))
}

fn read_bounded_exec_spec(fd: std::os::fd::RawFd, limit: usize) -> anyhow::Result<Vec<u8>> {
    let mut payload = Vec::new();
    let mut buffer = [0_u8; 4096];
    loop {
        let read = unsafe { libc::read(fd, buffer.as_mut_ptr().cast(), buffer.len()) };
        if read < 0 {
            let error = io::Error::last_os_error();
            if error.kind() == io::ErrorKind::Interrupted {
                continue;
            }
            return Err(error.into());
        }
        if read == 0 {
            break;
        }
        let read = usize::try_from(read)?;
        if payload.len().saturating_add(read) > limit {
            anyhow::bail!("exec config exceeds {limit} bytes");
        }
        payload.extend_from_slice(&buffer[..read]);
    }
    Ok(payload)
}

fn join_cgroup(cgroup_procs: &Path) -> anyhow::Result<()> {
    if cgroup_procs.file_name().and_then(|name| name.to_str()) != Some("cgroup.procs") {
        anyhow::bail!("exec config cgroup path must end in cgroup.procs");
    }
    let mut file = std::fs::OpenOptions::new().write(true).open(cgroup_procs)?;
    write!(file, "{}", std::process::id())?;
    Ok(())
}

fn apply_rlimits(limits: &RlimitSpec) -> anyhow::Result<()> {
    for (resource, value) in [
        (libc::RLIMIT_NPROC, limits.nproc),
        (libc::RLIMIT_NOFILE, limits.nofile),
        (libc::RLIMIT_FSIZE, limits.fsize_bytes),
        (libc::RLIMIT_CORE, limits.core_bytes),
    ] {
        let value = libc::rlim_t::try_from(value)?;
        let rlimit = libc::rlimit {
            rlim_cur: value,
            rlim_max: value,
        };
        if unsafe { libc::setrlimit(resource, &rlimit) } != 0 {
            return Err(io::Error::last_os_error().into());
        }
    }
    Ok(())
}

fn exec_target(spec: ExecSpec) -> anyhow::Result<std::convert::Infallible> {
    validate_environment(&spec.env)?;
    let mut command = std::process::Command::new(&spec.program);
    command.args(&spec.args);
    command.current_dir(&spec.cwd);
    command.env_clear();
    command.envs(spec.env);
    Err(command.exec().into())
}
