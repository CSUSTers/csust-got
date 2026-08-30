#[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
use super::*;

mod api;
pub use api::{SpawnedExecHelper, spawn_exec_helper};
#[cfg(all(feature = "c7-test-support", target_os = "linux"))]
pub(in crate::exec) mod c7_test_support;
#[cfg(target_os = "linux")]
pub(in crate::exec) mod fd_map;
#[cfg(target_os = "linux")]
pub use fd_map::install_exec_fds;
#[cfg(target_os = "linux")]
mod writer;

#[cfg(all(feature = "c7-test-support", target_os = "linux"))]
#[derive(Clone, Default)]
pub(super) struct SpawnControls {
    pub(super) fail_writer_creation: bool,
    pub(super) helper_spawn_marker: Option<Arc<std::sync::atomic::AtomicBool>>,
    pub(super) fd_install_fault: Option<fd_map::FdInstallStage>,
    pub(super) helper_args: Option<Vec<String>>,
    pub(super) fixture_cpu_stat: bool,
    pub(super) descriptor_probe: Option<c7_test_support::DescriptorReleaseProbe>,
}

#[cfg(any(test, feature = "c7-test-support"))]
pub(super) fn spawn_direct(
    target: ExecTarget,
    env: Vec<(String, String)>,
) -> Result<Child, SupervisorError> {
    let mut command = Command::new(target.program);
    command.args(target.args);
    command.current_dir(target.cwd);
    command.env_clear();
    command.envs(env);
    command.stdin(Stdio::null());
    command.stdout(Stdio::piped());
    command.stderr(Stdio::piped());
    command.kill_on_drop(true);
    #[cfg(unix)]
    command.process_group(0);
    #[cfg(target_os = "linux")]
    {
        let filters = sandbox::build_untrusted_seccomp()
            .map_err(|error| SupervisorError::Spawn(error.to_string()))?;
        unsafe {
            command.pre_exec(move || {
                sandbox::set_no_new_privs()
                    .and_then(|()| sandbox::apply_untrusted_seccomp_filters(&filters))
                    .map_err(|error| io::Error::other(error.to_string()))
            });
        }
    }
    command
        .spawn()
        .map_err(|error| SupervisorError::Spawn(format!("command failed: {error}")))
}

#[cfg(target_os = "linux")]
pub(super) fn spawn_exec_helper_with_control(
    binary: &Path,
    spec: &ExecSpec,
    control_source: std::os::fd::OwnedFd,
) -> Result<SpawnedExecHelper, ExecError> {
    spawn_exec_helper_with_control_inner(
        binary,
        spec,
        control_source,
        #[cfg(feature = "c7-test-support")]
        SpawnControls::default(),
    )
}

#[cfg(all(feature = "c7-test-support", target_os = "linux"))]
pub(super) fn spawn_exec_helper_with_control_and_controls(
    binary: &Path,
    spec: &ExecSpec,
    control_source: std::os::fd::OwnedFd,
    controls: SpawnControls,
) -> Result<SpawnedExecHelper, ExecError> {
    spawn_exec_helper_with_control_inner(binary, spec, control_source, controls)
}

#[cfg(target_os = "linux")]
fn spawn_exec_helper_with_control_inner(
    binary: &Path,
    spec: &ExecSpec,
    control_source: std::os::fd::OwnedFd,
    #[cfg(feature = "c7-test-support")] controls: SpawnControls,
) -> Result<SpawnedExecHelper, ExecError> {
    use std::os::fd::{AsRawFd as _, FromRawFd as _, OwnedFd};

    validate_environment(&spec.env)?;
    let payload = serde_json::to_vec(spec)
        .map_err(|error| ExecError::new(format!("serialize exec spec: {error}")))?;
    if payload.len() > MAX_EXEC_SPEC_BYTES {
        return Err(ExecError::new(format!(
            "exec spec exceeds {MAX_EXEC_SPEC_BYTES} bytes"
        )));
    }
    let mut pipe_fds = [-1; 2];
    if unsafe { libc::pipe2(pipe_fds.as_mut_ptr(), libc::O_CLOEXEC) } != 0 {
        return Err(ExecError::new(format!(
            "create exec config pipe: {}",
            io::Error::last_os_error()
        )));
    }
    let read_fd = unsafe { OwnedFd::from_raw_fd(pipe_fds[0]) };
    let write_fd = unsafe { OwnedFd::from_raw_fd(pipe_fds[1]) };
    let mut status_fds = [-1; 2];
    if unsafe { libc::pipe2(status_fds.as_mut_ptr(), libc::O_CLOEXEC | libc::O_NONBLOCK) } != 0 {
        return Err(ExecError::new(format!(
            "create exec status pipe: {}",
            io::Error::last_os_error()
        )));
    }
    let status_read = unsafe { OwnedFd::from_raw_fd(status_fds[0]) };
    let status_source = unsafe { OwnedFd::from_raw_fd(status_fds[1]) };
    #[cfg(feature = "c7-test-support")]
    c7_test_support::observe_spawn_descriptors(
        &controls,
        [
            &read_fd,
            &write_fd,
            &status_read,
            &status_source,
            &control_source,
        ],
    );
    let inherited_fd = read_fd.as_raw_fd();
    let inherited_control_fd = control_source.as_raw_fd();
    let inherited_status_fd = status_source.as_raw_fd();
    let writer = writer::spawn(
        write_fd,
        payload,
        #[cfg(feature = "c7-test-support")]
        controls.fail_writer_creation,
        #[cfg(not(feature = "c7-test-support"))]
        false,
    )?;
    let mut command = Command::new(binary);
    #[cfg(feature = "c7-test-support")]
    command.args(
        controls
            .helper_args
            .clone()
            .unwrap_or_else(|| helper_argv().to_vec()),
    );
    #[cfg(not(feature = "c7-test-support"))]
    command.args(helper_argv());
    command.env_clear();
    command.stdin(Stdio::null());
    command.stdout(Stdio::piped());
    command.stderr(Stdio::piped());
    command.kill_on_drop(true);
    command.process_group(0);
    #[cfg(feature = "c7-test-support")]
    let fd_install_fault = controls.fd_install_fault;
    unsafe {
        command.pre_exec(move || {
            #[cfg(feature = "c7-test-support")]
            if let Some(fault) = fd_install_fault {
                return fd_map::c7_test_support::install_exec_fds_with_fault(
                    inherited_fd,
                    inherited_control_fd,
                    inherited_status_fd,
                    fault,
                );
            }
            install_exec_fds(inherited_fd, inherited_control_fd, inherited_status_fd)
        });
    }
    #[cfg(feature = "c7-test-support")]
    if let Some(marker) = controls.helper_spawn_marker {
        marker.store(true, Ordering::SeqCst);
    }
    let mut child = match command.spawn() {
        Ok(child) => child,
        Err(error) => {
            drop(read_fd);
            drop(control_source);
            drop(status_source);
            let _ = writer.join();
            return Err(ExecError::new(format!("spawn exec helper: {error}")));
        }
    };
    drop(read_fd);
    drop(control_source);
    drop(status_source);
    match writer.join() {
        Ok(Ok(())) => {
            let status = ExecStartupStatusReader::from_descriptor(status_read).map_err(|_| {
                kill_wait_spawned_helper_blocking(&mut child);
                ExecError::new("initialize exec helper status reader")
            })?;
            Ok(SpawnedExecHelper { child, status })
        }
        Ok(Err(error)) => {
            kill_wait_spawned_helper_blocking(&mut child);
            Err(ExecError::new(format!("write exec helper config: {error}")))
        }
        Err(_) => {
            kill_wait_spawned_helper_blocking(&mut child);
            Err(ExecError::new("exec config writer panicked"))
        }
    }
}

#[cfg(target_os = "linux")]
fn kill_wait_spawned_helper_blocking(child: &mut Child) {
    let _ = child.start_kill();
    loop {
        match child.try_wait() {
            Ok(Some(_)) => return,
            Ok(None) => std::thread::sleep(Duration::from_millis(1)),
            Err(_) => return,
        }
    }
}

#[cfg(any(test, target_os = "linux"))]
pub(super) fn helper_argv() -> [String; 2] {
    [EXEC_CONFIG_FLAG.to_string(), EXEC_CONFIG_FD.to_string()]
}

#[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
pub(super) fn validate_environment(env: &[(String, String)]) -> Result<(), ExecError> {
    let allowed: BTreeSet<_> = ALLOWED_ENVIRONMENT.into_iter().collect();
    let mut seen = BTreeSet::new();
    for (name, _) in env {
        if !allowed.contains(name.as_str()) {
            return Err(ExecError::new(format!(
                "exec environment variable {name} is not allowed"
            )));
        }
        if !seen.insert(name) {
            return Err(ExecError::new(format!(
                "exec environment variable {name} is duplicated"
            )));
        }
    }
    Ok(())
}
