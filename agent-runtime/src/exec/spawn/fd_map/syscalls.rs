use super::{FdInstallStage, FdSyscalls};
use std::io;

pub(super) struct RealFdSyscalls;

impl FdSyscalls for RealFdSyscalls {
    fn duplicate(&mut self, fd: i32, _stage: FdInstallStage) -> io::Result<i32> {
        result_fd(unsafe { libc::fcntl(fd, libc::F_DUPFD_CLOEXEC, 6) })
    }

    fn close(&mut self, fd: i32, _stage: Option<FdInstallStage>) -> io::Result<()> {
        result_zero(unsafe { libc::close(fd) })
    }

    fn dup2(&mut self, source: i32, target: i32, _stage: FdInstallStage) -> io::Result<()> {
        result_fd(unsafe { libc::dup2(source, target) }).map(|_| ())
    }

    fn get_fd(&mut self, fd: i32, _stage: FdInstallStage) -> io::Result<i32> {
        result_fd(unsafe { libc::fcntl(fd, libc::F_GETFD) })
    }

    fn set_fd(&mut self, fd: i32, flags: i32, _stage: FdInstallStage) -> io::Result<()> {
        result_zero(unsafe { libc::fcntl(fd, libc::F_SETFD, flags) })
    }
}

fn result_fd(result: i32) -> io::Result<i32> {
    if result == -1 {
        Err(io::Error::last_os_error())
    } else {
        Ok(result)
    }
}

fn result_zero(result: i32) -> io::Result<()> {
    if result == -1 {
        Err(io::Error::last_os_error())
    } else {
        Ok(())
    }
}
