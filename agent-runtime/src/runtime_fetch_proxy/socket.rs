use super::RuntimeFetchProxyError;
use std::io;

pub(super) fn command_control_socket_pair()
-> Result<(std::os::fd::OwnedFd, std::os::fd::OwnedFd), RuntimeFetchProxyError> {
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
        return Err(RuntimeFetchProxyError::new(format!(
            "create command-control socket pair: {}",
            io::Error::last_os_error()
        )));
    }
    let runtime = unsafe { OwnedFd::from_raw_fd(descriptors[0]) };
    let command = unsafe { OwnedFd::from_raw_fd(descriptors[1]) };
    let flags = unsafe { libc::fcntl(descriptors[0], libc::F_GETFL) };
    if flags < 0
        || unsafe { libc::fcntl(descriptors[0], libc::F_SETFL, flags | libc::O_NONBLOCK) } < 0
    {
        return Err(RuntimeFetchProxyError::new(format!(
            "configure command-control runtime endpoint: {}",
            io::Error::last_os_error()
        )));
    }
    Ok((runtime, command))
}
