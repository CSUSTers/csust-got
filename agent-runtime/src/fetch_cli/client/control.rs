use crate::{
    exec::COMMAND_CONTROL_FD,
    fetch_cli::{FetchError, FetchErrorKind},
    runtime_fetch_proxy::{CommandControlPacket, MAX_COMMAND_CONTROL_PACKET_BYTES},
};
use std::{
    env, io,
    mem::{MaybeUninit, size_of},
    os::fd::{AsRawFd as _, FromRawFd as _, IntoRawFd as _, OwnedFd, RawFd},
};
use tokio::net::UnixStream;

pub(super) fn open_control_fd() -> Result<RawFd, FetchError> {
    let raw = env::var("AGENT_FETCH_CONTROL_FD")
        .map_err(|_| network("AGENT_FETCH_CONTROL_FD must be set to 4"))?;
    if raw.is_empty() || !raw.bytes().all(|byte| byte.is_ascii_digit()) {
        return Err(network(
            "AGENT_FETCH_CONTROL_FD must be a nonnegative decimal",
        ));
    }
    let descriptor: i32 = raw
        .parse()
        .map_err(|_| network("AGENT_FETCH_CONTROL_FD is out of range"))?;
    if descriptor != COMMAND_CONTROL_FD {
        return Err(network("AGENT_FETCH_CONTROL_FD must be fixed descriptor 4"));
    }
    validate_control_socket(descriptor)?;
    Ok(descriptor)
}

fn validate_control_socket(descriptor: RawFd) -> Result<(), FetchError> {
    let mut socket_type = 0_i32;
    let mut type_length = size_of::<i32>() as libc::socklen_t;
    if unsafe {
        libc::getsockopt(
            descriptor,
            libc::SOL_SOCKET,
            libc::SO_TYPE,
            (&mut socket_type as *mut i32).cast(),
            &mut type_length,
        )
    } != 0
        || type_length as usize != size_of::<i32>()
        || socket_type != libc::SOCK_SEQPACKET
    {
        return Err(network(
            "AGENT_FETCH_CONTROL_FD must be an open SOCK_SEQPACKET socket",
        ));
    }
    let mut address = MaybeUninit::<libc::sockaddr_storage>::zeroed();
    let mut address_length = size_of::<libc::sockaddr_storage>() as libc::socklen_t;
    if unsafe { libc::getsockname(descriptor, address.as_mut_ptr().cast(), &mut address_length) }
        != 0
    {
        return Err(network(format!(
            "inspect AGENT_FETCH_CONTROL_FD failed: {}",
            io::Error::last_os_error()
        )));
    }
    if unsafe { address.assume_init() }.ss_family as i32 != libc::AF_UNIX {
        return Err(network("AGENT_FETCH_CONTROL_FD must be an AF_UNIX socket"));
    }
    Ok(())
}

pub(super) fn create_and_transfer_session(
    control: RawFd,
    packet: &CommandControlPacket,
) -> Result<UnixStream, FetchError> {
    packet
        .validate()
        .map_err(|error| policy(format!("invalid fetch request metadata: {error}")))?;
    let payload = serde_json::to_vec(packet)
        .map_err(|error| network(format!("encode fetch request metadata failed: {error}")))?;
    if payload.len() > MAX_COMMAND_CONTROL_PACKET_BYTES {
        return Err(network(format!(
            "fetch request metadata exceeds {} byte limit",
            MAX_COMMAND_CONTROL_PACKET_BYTES
        )));
    }
    let (client, runtime) = session_pair()?;
    send_control_packet(control, &payload, runtime.as_raw_fd())?;
    drop(runtime);
    let raw = client.into_raw_fd();
    let standard = unsafe { std::os::unix::net::UnixStream::from_raw_fd(raw) };
    standard
        .set_nonblocking(true)
        .map_err(|error| network(format!("configure local fetch session failed: {error}")))?;
    UnixStream::from_std(standard)
        .map_err(|error| network(format!("open local fetch session failed: {error}")))
}

fn session_pair() -> Result<(OwnedFd, OwnedFd), FetchError> {
    let mut sockets = [-1_i32; 2];
    if unsafe {
        libc::socketpair(
            libc::AF_UNIX,
            libc::SOCK_STREAM | libc::SOCK_CLOEXEC,
            0,
            sockets.as_mut_ptr(),
        )
    } != 0
    {
        return Err(network(format!(
            "create local fetch session failed: {}",
            io::Error::last_os_error()
        )));
    }
    Ok(unsafe {
        (
            OwnedFd::from_raw_fd(sockets[0]),
            OwnedFd::from_raw_fd(sockets[1]),
        )
    })
}

fn send_control_packet(control: RawFd, payload: &[u8], session: RawFd) -> Result<(), FetchError> {
    let mut iovec = libc::iovec {
        iov_base: payload.as_ptr().cast_mut().cast(),
        iov_len: payload.len(),
    };
    let mut ancillary = [MaybeUninit::<libc::cmsghdr>::uninit(); 2];
    let mut message: libc::msghdr = unsafe { std::mem::zeroed() };
    message.msg_iov = &mut iovec;
    message.msg_iovlen = 1;
    message.msg_control = ancillary.as_mut_ptr().cast();
    message.msg_controllen = unsafe { libc::CMSG_SPACE(size_of::<i32>() as u32) as usize };
    let header = unsafe { libc::CMSG_FIRSTHDR(&message) };
    if header.is_null() {
        return Err(network("create SCM_RIGHTS control message failed"));
    }
    unsafe {
        (*header).cmsg_level = libc::SOL_SOCKET;
        (*header).cmsg_type = libc::SCM_RIGHTS;
        (*header).cmsg_len = libc::CMSG_LEN(size_of::<i32>() as u32) as usize;
        *libc::CMSG_DATA(header).cast::<i32>() = session;
    }
    let sent = unsafe { libc::sendmsg(control, &message, libc::MSG_DONTWAIT | libc::MSG_NOSIGNAL) };
    if sent < 0 {
        return Err(network(format!(
            "transfer local fetch session failed: {}",
            io::Error::last_os_error()
        )));
    }
    if sent as usize != payload.len() {
        return Err(network("partial command-control packet transfer"));
    }
    Ok(())
}

fn policy(message: impl Into<String>) -> FetchError {
    FetchError::new(FetchErrorKind::Policy, message)
}

fn network(message: impl Into<String>) -> FetchError {
    FetchError::new(FetchErrorKind::NetworkProtocol, message)
}
