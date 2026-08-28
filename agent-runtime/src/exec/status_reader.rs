#[cfg(any(test, target_os = "linux", feature = "c7-test-support"))]
use super::status::decode_payload;
use super::{ExecStartupChannelError, ExecStartupOutcome};
#[cfg(any(test, feature = "c7-test-support"))]
use std::pin::Pin;
use std::time::Duration;
#[cfg(any(test, feature = "c7-test-support"))]
use tokio::io::{AsyncRead, AsyncReadExt as _};

pub(super) enum ExecStartupStatusReader {
    #[cfg(target_os = "linux")]
    Descriptor(tokio::io::unix::AsyncFd<std::os::fd::OwnedFd>),
    #[cfg(any(test, feature = "c7-test-support"))]
    Stream(Pin<Box<dyn AsyncRead + Send>>),
    #[cfg(all(not(test), not(target_os = "linux"), not(feature = "c7-test-support")))]
    #[allow(dead_code)]
    Unsupported,
}

impl ExecStartupStatusReader {
    #[cfg(target_os = "linux")]
    pub(super) fn from_descriptor(
        descriptor: std::os::fd::OwnedFd,
    ) -> Result<Self, std::io::Error> {
        tokio::io::unix::AsyncFd::new(descriptor).map(Self::Descriptor)
    }

    #[cfg(any(test, feature = "c7-test-support"))]
    pub(super) fn from_stream(stream: Pin<Box<dyn AsyncRead + Send>>) -> Self {
        Self::Stream(stream)
    }

    pub(super) async fn wait_for_startup(
        &mut self,
        deadline: Duration,
    ) -> Result<ExecStartupOutcome, ExecStartupChannelError> {
        tokio::time::timeout(deadline, self.read_to_eof())
            .await
            .map_err(|_| ExecStartupChannelError::Timeout)?
    }

    async fn read_to_eof(&mut self) -> Result<ExecStartupOutcome, ExecStartupChannelError> {
        match self {
            #[cfg(target_os = "linux")]
            Self::Descriptor(descriptor) => read_descriptor_to_eof(descriptor).await,
            #[cfg(any(test, feature = "c7-test-support"))]
            Self::Stream(stream) => {
                let mut payload = [0_u8; 5];
                let mut length = 0_usize;
                loop {
                    if length == payload.len() {
                        return Err(ExecStartupChannelError::Malformed);
                    }
                    let count = stream
                        .read(&mut payload[length..])
                        .await
                        .map_err(ExecStartupChannelError::ReadFailed)?;
                    if count == 0 {
                        break;
                    }
                    length += count;
                }
                decode_payload(&payload[..length])
            }
            #[cfg(all(not(test), not(target_os = "linux"), not(feature = "c7-test-support")))]
            Self::Unsupported => Err(ExecStartupChannelError::ReadFailed(std::io::Error::new(
                std::io::ErrorKind::Unsupported,
                "exec startup status requires Linux",
            ))),
        }
    }
}

#[cfg(target_os = "linux")]
async fn read_descriptor_to_eof(
    descriptor: &tokio::io::unix::AsyncFd<std::os::fd::OwnedFd>,
) -> Result<ExecStartupOutcome, ExecStartupChannelError> {
    use std::os::fd::AsRawFd as _;

    let mut payload = [0_u8; 5];
    let mut length = 0_usize;
    loop {
        if length == payload.len() {
            return Err(ExecStartupChannelError::Malformed);
        }
        let mut ready = descriptor
            .readable()
            .await
            .map_err(ExecStartupChannelError::ReadFailed)?;
        let read = ready.try_io(|inner| {
            let count = unsafe {
                libc::read(
                    inner.get_ref().as_raw_fd(),
                    payload[length..].as_mut_ptr().cast(),
                    payload.len() - length,
                )
            };
            if count < 0 {
                Err(std::io::Error::last_os_error())
            } else {
                usize::try_from(count).map_err(|_| std::io::ErrorKind::InvalidData.into())
            }
        });
        match read {
            Ok(Ok(0)) => break,
            Ok(Ok(count)) => length += count,
            Ok(Err(error)) => return Err(ExecStartupChannelError::ReadFailed(error)),
            Err(_) => continue,
        }
    }
    decode_payload(&payload[..length])
}
