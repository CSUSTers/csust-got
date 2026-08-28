use super::super::*;

pub struct SpawnedExecHelper {
    pub child: Child,
    pub(in crate::exec) status: ExecStartupStatusReader,
}

impl SpawnedExecHelper {
    pub async fn wait_for_startup(
        &mut self,
    ) -> Result<ExecStartupOutcome, ExecStartupChannelError> {
        self.status.wait_for_startup(EXEC_STARTUP_TIMEOUT).await
    }

    pub async fn await_startup_status(
        &mut self,
        deadline: Duration,
    ) -> Result<ExecStartupOutcome, ExecStartupChannelError> {
        self.status.wait_for_startup(deadline).await
    }
}

pub fn spawn_exec_helper(binary: &Path, spec: &ExecSpec) -> Result<SpawnedExecHelper, ExecError> {
    #[cfg(not(target_os = "linux"))]
    {
        let _ = (binary, spec);
        return Err(ExecError::new(
            "agent runtime production execution requires Linux",
        ));
    }
    #[cfg(target_os = "linux")]
    {
        let control_source = unavailable_control_source()?;
        super::spawn_exec_helper_with_control(binary, spec, control_source)
    }
}

#[cfg(target_os = "linux")]
fn unavailable_control_source() -> Result<std::os::fd::OwnedFd, ExecError> {
    use std::os::fd::{FromRawFd as _, OwnedFd};
    let mut descriptors = [-1_i32; 2];
    if unsafe {
        libc::socketpair(
            libc::AF_UNIX,
            libc::SOCK_SEQPACKET | libc::SOCK_CLOEXEC,
            0,
            descriptors.as_mut_ptr(),
        )
    } != 0
    {
        return Err(ExecError::new(format!(
            "create unavailable command-control socket: {}",
            io::Error::last_os_error()
        )));
    }
    let runtime = unsafe { OwnedFd::from_raw_fd(descriptors[0]) };
    let command = unsafe { OwnedFd::from_raw_fd(descriptors[1]) };
    drop(runtime);
    Ok(command)
}
