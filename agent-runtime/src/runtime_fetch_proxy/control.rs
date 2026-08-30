use super::{
    CommandBindingPhase, MAX_COMMAND_CONTROL_PACKET_BYTES, MAX_COMMAND_CONTROL_PACKETS,
    RuntimeFetchProxyError, registry::ControlReport,
};
use std::{io, sync::Arc};
use tokio::sync::mpsc;
use tokio_util::sync::CancellationToken;

#[cfg(any(test, feature = "c7-test-support"))]
mod fault;
#[cfg(feature = "c7-test-support")]
pub(super) use fault::BlockingSessionFault;
#[cfg(any(test, feature = "c7-test-support"))]
pub(super) use fault::SessionFault;

pub(super) struct SessionJob {
    pub(super) packet: ReceivedControlPacket,
    pub(super) permit: tokio::sync::OwnedSemaphorePermit,
    #[cfg(any(test, feature = "c7-test-support"))]
    pub(super) fault: Option<SessionFault>,
}

pub(super) async fn control_reader(
    endpoint: std::os::fd::OwnedFd,
    phase: Arc<std::sync::Mutex<CommandBindingPhase>>,
    permits: Arc<tokio::sync::Semaphore>,
    cancel: CancellationToken,
    jobs: mpsc::Sender<SessionJob>,
) -> Result<ControlReport, RuntimeFetchProxyError> {
    let mut endpoint = match tokio::io::unix::AsyncFd::new(endpoint) {
        Ok(endpoint) => Some(endpoint),
        Err(_) => {
            return Err(RuntimeFetchProxyError::new(
                "command binding control reader could not start",
            ));
        }
    };
    let mut packets = 0_usize;
    loop {
        tokio::select! {
            biased;
            _ = cancel.cancelled() => break,
            packet = receive_next_control_packet(endpoint.as_ref()) => {
                packets += 1;
                if packets >= MAX_COMMAND_CONTROL_PACKETS {
                    endpoint.take();
                }
                let packet = match packet {
                    Ok(packet) => packet,
                    Err(ControlReceiveError::Rejected) => continue,
                    Err(ControlReceiveError::Closed(error)) => return Err(error),
                };
                let packet = match packet.into_parsed() {
                    Ok(packet) => packet,
                    Err(_) => continue,
                };
                let _ = with_active_binding(&phase, || {
                    let Ok(permit) = Arc::clone(&permits).try_acquire_owned() else {
                        return false;
                    };
                    jobs.try_send(SessionJob {
                        packet,
                        permit,
                        #[cfg(any(test, feature = "c7-test-support"))]
                        fault: None,
                    }).is_ok()
                });
            }
        }
    }
    Ok(ControlReport)
}

fn with_active_binding<T>(
    phase: &Arc<std::sync::Mutex<CommandBindingPhase>>,
    action: impl FnOnce() -> T,
) -> Result<Option<T>, RuntimeFetchProxyError> {
    let phase = phase
        .lock()
        .map_err(|_| RuntimeFetchProxyError::new("command binding phase is poisoned"))?;
    if *phase == CommandBindingPhase::Active {
        Ok(Some(action()))
    } else {
        Ok(None)
    }
}

pub(super) struct ReceivedControlPacket {
    pub(super) metadata: super::CommandControlPacket,
    pub(super) stream: std::os::fd::OwnedFd,
}

struct RawControlPacket {
    payload: Vec<u8>,
    stream: std::os::fd::OwnedFd,
}

impl RawControlPacket {
    fn into_parsed(self) -> Result<ReceivedControlPacket, RuntimeFetchProxyError> {
        let metadata: super::CommandControlPacket = serde_json::from_slice(&self.payload)
            .map_err(|_| RuntimeFetchProxyError::new("malformed control metadata"))?;
        metadata.validate()?;
        Ok(ReceivedControlPacket {
            metadata,
            stream: self.stream,
        })
    }
}

async fn receive_control_packet(
    endpoint: &tokio::io::unix::AsyncFd<std::os::fd::OwnedFd>,
) -> Result<RawControlPacket, ControlReceiveError> {
    loop {
        let mut ready = endpoint.readable().await.map_err(|_| {
            ControlReceiveError::Closed(RuntimeFetchProxyError::new("control receive failed"))
        })?;
        match ready.try_io(|inner| recv_control_packet(inner.get_ref())) {
            Ok(Ok(packet)) => return Ok(packet),
            Ok(Err(error)) if error.kind() == io::ErrorKind::InvalidData => {
                return Err(ControlReceiveError::Rejected);
            }
            Ok(Err(_)) => {
                return Err(ControlReceiveError::Closed(RuntimeFetchProxyError::new(
                    "control receive failed",
                )));
            }
            Err(_) => continue,
        }
    }
}

async fn receive_next_control_packet(
    endpoint: Option<&tokio::io::unix::AsyncFd<std::os::fd::OwnedFd>>,
) -> Result<RawControlPacket, ControlReceiveError> {
    match endpoint {
        Some(endpoint) => receive_control_packet(endpoint).await,
        None => std::future::pending().await,
    }
}

fn recv_control_packet(endpoint: &std::os::fd::OwnedFd) -> io::Result<RawControlPacket> {
    use std::mem::{MaybeUninit, size_of};
    use std::os::fd::{AsRawFd as _, FromRawFd as _, OwnedFd};

    let mut payload = [0_u8; MAX_COMMAND_CONTROL_PACKET_BYTES];
    let mut control = [MaybeUninit::<libc::cmsghdr>::uninit(); 16];
    let mut iovec = libc::iovec {
        iov_base: payload.as_mut_ptr().cast(),
        iov_len: payload.len(),
    };
    let mut message: libc::msghdr = unsafe { std::mem::zeroed() };
    message.msg_iov = &mut iovec;
    message.msg_iovlen = 1;
    message.msg_control = control.as_mut_ptr().cast();
    message.msg_controllen = size_of::<[MaybeUninit<libc::cmsghdr>; 16]>();
    let received =
        unsafe { libc::recvmsg(endpoint.as_raw_fd(), &mut message, libc::MSG_CMSG_CLOEXEC) };
    if received < 0 {
        return Err(io::Error::last_os_error());
    }
    let mut descriptor = None::<OwnedFd>;
    let mut unknown_ancillary = false;
    unsafe {
        let control_start = message.msg_control as usize;
        let control_end = control_start.saturating_add(message.msg_controllen);
        let mut header = libc::CMSG_FIRSTHDR(&message);
        while !header.is_null() {
            let header_start = header as usize;
            let header_length = (*header).cmsg_len;
            if header_length < libc::CMSG_LEN(0) as usize
                || header_start < control_start
                || header_start.saturating_add(header_length) > control_end
            {
                unknown_ancillary = true;
                break;
            }
            if (*header).cmsg_level == libc::SOL_SOCKET && (*header).cmsg_type == libc::SCM_RIGHTS {
                let data_bytes = header_length - libc::CMSG_LEN(0) as usize;
                if data_bytes % size_of::<i32>() != 0 {
                    unknown_ancillary = true;
                } else {
                    let count = data_bytes / size_of::<i32>();
                    let data = libc::CMSG_DATA(header).cast::<i32>();
                    for index in 0..count {
                        let received = OwnedFd::from_raw_fd(*data.add(index));
                        if descriptor.is_none() {
                            descriptor = Some(received);
                        } else {
                            unknown_ancillary = true;
                        }
                    }
                }
            } else {
                unknown_ancillary = true;
            }
            header = libc::CMSG_NXTHDR(&message, header);
        }
    }
    if received == 0 {
        return Err(io::Error::new(
            io::ErrorKind::UnexpectedEof,
            "command control endpoint closed",
        ));
    }
    if message.msg_flags & (libc::MSG_TRUNC | libc::MSG_CTRUNC) != 0
        || unknown_ancillary
        || descriptor.is_none()
    {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "control packet must be untruncated with exactly one SCM_RIGHTS descriptor",
        ));
    }
    let received = usize::try_from(received).map_err(|_| io::ErrorKind::InvalidData)?;
    let stream = descriptor.expect("exact descriptor count checked");
    let mut socket_type = 0_i32;
    let mut length = size_of::<i32>() as libc::socklen_t;
    if unsafe {
        libc::getsockopt(
            stream.as_raw_fd(),
            libc::SOL_SOCKET,
            libc::SO_TYPE,
            (&mut socket_type as *mut i32).cast(),
            &mut length,
        )
    } != 0
        || socket_type != libc::SOCK_STREAM
    {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "control descriptor must be a SOCK_STREAM socket",
        ));
    }
    Ok(RawControlPacket {
        payload: payload[..received].to_vec(),
        stream,
    })
}

enum ControlReceiveError {
    Rejected,
    Closed(RuntimeFetchProxyError),
}

#[cfg(test)]
mod tests;
