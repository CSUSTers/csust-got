use super::{FdInstallStage, FdRole, FdSyscalls, install_exec_fds_with, syscalls::RealFdSyscalls};
use std::io;

pub const FD_INSTALL_FAULT_STAGES: [&str; 21] = [
    "duplicate-config",
    "duplicate-control",
    "duplicate-status",
    "original-close-config",
    "original-close-control",
    "original-close-status",
    "dup2-config",
    "dup2-control",
    "dup2-status",
    "getfd-config",
    "getfd-control",
    "getfd-status",
    "setfd-config",
    "setfd-control",
    "setfd-status",
    "verify-getfd-config",
    "verify-getfd-control",
    "verify-getfd-status",
    "temp-close-config",
    "temp-close-control",
    "temp-close-status",
];

pub(in crate::exec) const FD_INSTALL_FAULTS: [FdInstallStage; 21] = [
    FdInstallStage::Duplicate(FdRole::Config),
    FdInstallStage::Duplicate(FdRole::Control),
    FdInstallStage::Duplicate(FdRole::Status),
    FdInstallStage::OriginalClose(FdRole::Config),
    FdInstallStage::OriginalClose(FdRole::Control),
    FdInstallStage::OriginalClose(FdRole::Status),
    FdInstallStage::Dup2(FdRole::Config),
    FdInstallStage::Dup2(FdRole::Control),
    FdInstallStage::Dup2(FdRole::Status),
    FdInstallStage::GetFd(FdRole::Config),
    FdInstallStage::GetFd(FdRole::Control),
    FdInstallStage::GetFd(FdRole::Status),
    FdInstallStage::SetFd(FdRole::Config),
    FdInstallStage::SetFd(FdRole::Control),
    FdInstallStage::SetFd(FdRole::Status),
    FdInstallStage::VerifyGetFd(FdRole::Config),
    FdInstallStage::VerifyGetFd(FdRole::Control),
    FdInstallStage::VerifyGetFd(FdRole::Status),
    FdInstallStage::TempClose(FdRole::Config),
    FdInstallStage::TempClose(FdRole::Control),
    FdInstallStage::TempClose(FdRole::Status),
];

pub(in crate::exec) unsafe fn install_exec_fds_with_fault(
    config_source: i32,
    control_source: i32,
    status_source: i32,
    fault: FdInstallStage,
) -> io::Result<()> {
    unsafe {
        install_exec_fds_with(
            &mut FaultingFdSyscalls {
                fault,
                real: RealFdSyscalls,
            },
            config_source,
            control_source,
            status_source,
        )
    }
}

struct FaultingFdSyscalls {
    fault: FdInstallStage,
    real: RealFdSyscalls,
}

impl FdSyscalls for FaultingFdSyscalls {
    fn duplicate(&mut self, fd: i32, stage: FdInstallStage) -> io::Result<i32> {
        if stage == self.fault {
            Err(injected_error())
        } else {
            self.real.duplicate(fd, stage)
        }
    }

    fn close(&mut self, fd: i32, stage: Option<FdInstallStage>) -> io::Result<()> {
        let result = self.real.close(fd, stage);
        if stage == Some(self.fault) {
            Err(injected_error())
        } else {
            result
        }
    }

    fn dup2(&mut self, source: i32, target: i32, stage: FdInstallStage) -> io::Result<()> {
        if stage == self.fault {
            Err(injected_error())
        } else {
            self.real.dup2(source, target, stage)
        }
    }

    fn get_fd(&mut self, fd: i32, stage: FdInstallStage) -> io::Result<i32> {
        if stage == self.fault {
            Err(injected_error())
        } else {
            self.real.get_fd(fd, stage)
        }
    }

    fn set_fd(&mut self, fd: i32, flags: i32, stage: FdInstallStage) -> io::Result<()> {
        if stage == self.fault {
            Err(injected_error())
        } else {
            self.real.set_fd(fd, flags, stage)
        }
    }
}

fn injected_error() -> io::Error {
    io::Error::from_raw_os_error(libc::EIO)
}
