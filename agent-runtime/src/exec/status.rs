use std::{fmt, io, time::Duration};
use tokio::io::{AsyncRead, AsyncReadExt as _};

pub const EXEC_STATUS_RECORD_BYTES: usize = 4;
pub const EXEC_STARTUP_TIMEOUT: Duration = Duration::from_secs(2);
const EXEC_STATUS_VERSION: u8 = 1;
const EXEC_STATUS_FAILURE_KIND: u8 = 1;
const EXEC_STATUS_READ_BYTES: usize = EXEC_STATUS_RECORD_BYTES + 1;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u8)]
pub enum ExecInitStage {
    StatusCloexec = 1,
    ConfigRead = 2,
    ConfigDecode = 3,
    ConfigClose = 4,
    CgroupJoin = 5,
    Rlimit = 6,
    CloseInheritedFds = 7,
    NoNewPrivs = 8,
    Seccomp = 9,
    TargetExec = 10,
}

impl ExecInitStage {
    pub const ALL: [Self; 10] = [
        Self::StatusCloexec,
        Self::ConfigRead,
        Self::ConfigDecode,
        Self::ConfigClose,
        Self::CgroupJoin,
        Self::Rlimit,
        Self::CloseInheritedFds,
        Self::NoNewPrivs,
        Self::Seccomp,
        Self::TargetExec,
    ];

    fn from_wire(value: u8) -> Option<Self> {
        Self::ALL.into_iter().find(|stage| *stage as u8 == value)
    }
}

pub type ExecInitFailureStage = ExecInitStage;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ExecStatusRecord {
    pub stage: ExecInitStage,
}

impl ExecStatusRecord {
    pub fn encode(self) -> [u8; EXEC_STATUS_RECORD_BYTES] {
        [
            EXEC_STATUS_VERSION,
            EXEC_STATUS_FAILURE_KIND,
            self.stage as u8,
            0,
        ]
    }

    pub fn decode(bytes: [u8; EXEC_STATUS_RECORD_BYTES]) -> Result<Self, ExecStartupChannelError> {
        if bytes[0] != EXEC_STATUS_VERSION || bytes[1] != EXEC_STATUS_FAILURE_KIND || bytes[3] != 0
        {
            return Err(ExecStartupChannelError::Malformed);
        }
        let stage = ExecInitStage::from_wire(bytes[2]).ok_or(ExecStartupChannelError::Malformed)?;
        Ok(Self { stage })
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ExecStartupOutcome {
    TargetExecSucceeded,
    HelperFailed(ExecStatusRecord),
}

#[derive(Debug)]
pub enum ExecStartupChannelError {
    Timeout,
    Malformed,
    ReadFailed(io::Error),
}

impl fmt::Display for ExecStartupChannelError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(match self {
            Self::Timeout => "exec helper status deadline exceeded",
            Self::Malformed => "exec helper status is malformed",
            Self::ReadFailed(_) => "exec helper status read failed",
        })
    }
}

impl std::error::Error for ExecStartupChannelError {}

pub type ExecStatusError = ExecStartupChannelError;
pub type ExecStatusOutcome = ExecStartupOutcome;

pub fn encode_exec_status_failure(stage: ExecInitStage) -> [u8; EXEC_STATUS_RECORD_BYTES] {
    ExecStatusRecord { stage }.encode()
}

pub async fn await_exec_status<R>(
    reader: &mut R,
    deadline: Duration,
) -> Result<ExecStartupOutcome, ExecStartupChannelError>
where
    R: AsyncRead + Unpin,
{
    tokio::time::timeout(deadline, read_async_to_eof(reader))
        .await
        .map_err(|_| ExecStartupChannelError::Timeout)?
}

async fn read_async_to_eof<R>(reader: &mut R) -> Result<ExecStartupOutcome, ExecStartupChannelError>
where
    R: AsyncRead + Unpin,
{
    let mut payload = [0_u8; EXEC_STATUS_READ_BYTES];
    let mut length = 0_usize;
    loop {
        if length == payload.len() {
            return Err(ExecStartupChannelError::Malformed);
        }
        let count = reader
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

pub(super) fn decode_payload(
    payload: &[u8],
) -> Result<ExecStartupOutcome, ExecStartupChannelError> {
    match payload {
        [] => Ok(ExecStartupOutcome::TargetExecSucceeded),
        bytes if bytes.len() == EXEC_STATUS_RECORD_BYTES => {
            let record = ExecStatusRecord::decode(
                bytes
                    .try_into()
                    .map_err(|_| ExecStartupChannelError::Malformed)?,
            )?;
            Ok(ExecStartupOutcome::HelperFailed(record))
        }
        _ => Err(ExecStartupChannelError::Malformed),
    }
}

#[cfg(test)]
pub(super) async fn read_exec_startup_for_test(
    payload: Vec<u8>,
    deadline: Duration,
) -> Result<ExecStartupOutcome, ExecStartupChannelError> {
    let mut reader = std::io::Cursor::new(payload);
    await_exec_status(&mut reader, deadline).await
}

#[cfg(target_os = "linux")]
pub fn write_exec_status_failure(fd: std::os::fd::RawFd, stage: ExecInitStage) -> io::Result<()> {
    let payload = ExecStatusRecord { stage }.encode();
    loop {
        let written = unsafe { libc::write(fd, payload.as_ptr().cast(), payload.len()) };
        if written < 0 {
            let error = io::Error::last_os_error();
            if error.kind() == io::ErrorKind::Interrupted {
                continue;
            }
            return Err(error);
        }
        if usize::try_from(written).ok() != Some(payload.len()) {
            return Err(io::Error::new(
                io::ErrorKind::WriteZero,
                "exec status record write was not atomic",
            ));
        }
        return Ok(());
    }
}
