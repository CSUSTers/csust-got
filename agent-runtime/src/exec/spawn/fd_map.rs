mod syscalls;

#[cfg(all(feature = "c7-test-support", target_os = "linux"))]
pub(crate) mod c7_test_support;

use self::syscalls::RealFdSyscalls;
use super::super::{COMMAND_CONTROL_FD, EXEC_CONFIG_FD, EXEC_STATUS_FD};
use std::io;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum FdRole {
    Config,
    Control,
    Status,
}

impl FdRole {
    const ALL: [Self; 3] = [Self::Config, Self::Control, Self::Status];
}

const OWNED_DESCRIPTOR_GROUPS: usize = 3;
const RETIRED_DESCRIPTOR_CAPACITY: usize = OWNED_DESCRIPTOR_GROUPS * FdRole::ALL.len();

struct RetiredDescriptors {
    values: [i32; RETIRED_DESCRIPTOR_CAPACITY],
}

impl RetiredDescriptors {
    const fn new() -> Self {
        Self {
            values: [-1; RETIRED_DESCRIPTOR_CAPACITY],
        }
    }

    fn insert(&mut self, fd: i32) -> io::Result<bool> {
        if self.values.contains(&fd) {
            return Ok(false);
        }
        if let Some(slot) = self.values.iter_mut().find(|slot| **slot < 0) {
            *slot = fd;
            return Ok(true);
        }
        Err(io::Error::from_raw_os_error(libc::EOVERFLOW))
    }

    fn remove(&mut self, fd: i32) {
        if let Some(slot) = self.values.iter_mut().find(|slot| **slot == fd) {
            *slot = -1;
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum FdInstallStage {
    Duplicate(FdRole),
    OriginalClose(FdRole),
    Dup2(FdRole),
    GetFd(FdRole),
    SetFd(FdRole),
    VerifyGetFd(FdRole),
    TempClose(FdRole),
}

pub(crate) trait FdSyscalls {
    fn duplicate(&mut self, fd: i32, stage: FdInstallStage) -> io::Result<i32>;
    fn close(&mut self, fd: i32, stage: Option<FdInstallStage>) -> io::Result<()>;
    fn dup2(&mut self, source: i32, target: i32, stage: FdInstallStage) -> io::Result<()>;
    fn get_fd(&mut self, fd: i32, stage: FdInstallStage) -> io::Result<i32>;
    fn set_fd(&mut self, fd: i32, flags: i32, stage: FdInstallStage) -> io::Result<()>;
}

pub unsafe fn install_exec_fds(
    config_source: i32,
    control_source: i32,
    status_source: i32,
) -> io::Result<()> {
    unsafe {
        install_exec_fds_with(
            &mut RealFdSyscalls,
            config_source,
            control_source,
            status_source,
        )
    }
}

pub(crate) unsafe fn install_exec_fds_with<S: FdSyscalls>(
    syscalls: &mut S,
    config_source: i32,
    control_source: i32,
    status_source: i32,
) -> io::Result<()> {
    let mut sources = [config_source, control_source, status_source];
    let targets = [EXEC_CONFIG_FD, COMMAND_CONTROL_FD, EXEC_STATUS_FD];
    let mut temps = [-1_i32; 3];
    let mut retired = RetiredDescriptors::new();
    if sources.iter().any(|fd| *fd < 0)
        || config_source == control_source
        || config_source == status_source
        || control_source == status_source
    {
        cleanup_owned(syscalls, [&sources, &temps, &targets], &mut retired);
        return Err(io::Error::from_raw_os_error(libc::EINVAL));
    }

    for (index, role) in FdRole::ALL.into_iter().enumerate() {
        match syscalls.duplicate(sources[index], FdInstallStage::Duplicate(role)) {
            Ok(temp) => temps[index] = temp,
            Err(error) => {
                cleanup_owned(syscalls, [&sources, &temps, &targets], &mut retired);
                return Err(error);
            }
        }
    }

    if let Some(error) = close_phase(
        syscalls,
        &mut sources,
        FdInstallStage::OriginalClose,
        &mut retired,
    ) {
        cleanup_owned(syscalls, [&sources, &temps, &targets], &mut retired);
        return Err(error);
    }

    for (index, role) in FdRole::ALL.into_iter().enumerate() {
        let target = targets[index];
        if let Err(error) = syscalls.dup2(temps[index], target, FdInstallStage::Dup2(role)) {
            cleanup_owned(syscalls, [&sources, &temps, &targets], &mut retired);
            return Err(error);
        }
        retired.remove(target);
        if let Err(error) = clear_and_verify_cloexec(syscalls, target, role) {
            cleanup_owned(syscalls, [&sources, &temps, &targets], &mut retired);
            return Err(error);
        }
    }

    if let Some(error) = close_phase(
        syscalls,
        &mut temps,
        FdInstallStage::TempClose,
        &mut retired,
    ) {
        cleanup_owned(syscalls, [&sources, &temps, &targets], &mut retired);
        return Err(error);
    }
    Ok(())
}

fn clear_and_verify_cloexec<S: FdSyscalls>(
    syscalls: &mut S,
    fd: i32,
    role: FdRole,
) -> io::Result<()> {
    let flags = syscalls.get_fd(fd, FdInstallStage::GetFd(role))?;
    syscalls.set_fd(fd, flags & !libc::FD_CLOEXEC, FdInstallStage::SetFd(role))?;
    let verified = syscalls.get_fd(fd, FdInstallStage::VerifyGetFd(role))?;
    if verified & libc::FD_CLOEXEC != 0 {
        return Err(io::Error::from_raw_os_error(libc::EIO));
    }
    Ok(())
}

fn close_phase<S: FdSyscalls>(
    syscalls: &mut S,
    descriptors: &mut [i32; 3],
    stage: fn(FdRole) -> FdInstallStage,
    retired: &mut RetiredDescriptors,
) -> Option<io::Error> {
    let mut first_error = None;
    for (index, role) in FdRole::ALL.into_iter().enumerate() {
        let fd = std::mem::replace(&mut descriptors[index], -1);
        if fd >= 0 {
            match retired.insert(fd) {
                Ok(true) => {
                    if let Err(error) = syscalls.close(fd, Some(stage(role))) {
                        first_error.get_or_insert(error);
                    }
                }
                Ok(false) => {}
                Err(error) => {
                    first_error.get_or_insert(error);
                    if let Err(error) = syscalls.close(fd, Some(stage(role))) {
                        first_error.get_or_insert(error);
                    }
                }
            }
        }
    }
    first_error
}

fn cleanup_owned<S: FdSyscalls>(
    syscalls: &mut S,
    groups: [&[i32; 3]; 3],
    retired: &mut RetiredDescriptors,
) {
    for descriptors in groups {
        for fd in descriptors {
            if *fd >= 0 {
                match retired.insert(*fd) {
                    Ok(true) | Err(_) => {
                        let _ = syscalls.close(*fd, None);
                    }
                    Ok(false) => {}
                }
            }
        }
    }
}
