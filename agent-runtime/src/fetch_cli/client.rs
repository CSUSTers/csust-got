#[cfg(target_os = "linux")]
mod control;
#[cfg(all(target_os = "linux", feature = "c7-test-support"))]
pub(in crate::fetch_cli) mod session;
#[cfg(all(target_os = "linux", not(feature = "c7-test-support")))]
mod session;

use super::{FetchCli, FetchError, FetchErrorKind};
use tokio::io::{AsyncRead, AsyncWrite};

#[cfg(target_os = "linux")]
use super::body::PreparedBody;
#[cfg(target_os = "linux")]
use crate::{
    fetch_protocol::{LocalClientFrame, write_local_client_frame},
    runtime_fetch_proxy::{COMMAND_CONTROL_CANCEL_GRACE, LocalRequestState},
};
#[cfg(target_os = "linux")]
use std::{sync::Arc, time::Duration};
#[cfg(target_os = "linux")]
use tokio::{
    io::AsyncWriteExt as _,
    net::unix::OwnedWriteHalf,
    sync::Mutex,
    time::{Instant, sleep_until, timeout},
};

#[cfg(target_os = "linux")]
struct WriterState {
    writer: OwnedWriteHalf,
    request: LocalRequestState,
}

#[cfg(target_os = "linux")]
pub(crate) struct SharedWriter {
    inner: Mutex<WriterState>,
}

#[cfg(target_os = "linux")]
impl SharedWriter {
    fn new(writer: OwnedWriteHalf) -> Self {
        Self {
            inner: Mutex::new(WriterState {
                writer,
                request: LocalRequestState::default(),
            }),
        }
    }

    pub async fn continued(&self) -> Result<(), FetchError> {
        self.inner
            .lock()
            .await
            .request
            .continued()
            .map_err(state_error)
    }

    pub async fn send(&self, frame: &LocalClientFrame) -> Result<(), FetchError> {
        let mut state = self.inner.lock().await;
        match frame {
            LocalClientFrame::BodyChunk(bytes) => {
                state.request.body_chunk(bytes.len()).map_err(state_error)?
            }
            LocalClientFrame::BodyEnd => state.request.body_end().map_err(state_error)?,
            LocalClientFrame::Cancel => state.request.cancel().map_err(state_error)?,
        }
        write_local_client_frame(&mut state.writer, frame)
            .await
            .map_err(|error| network(format!("fetch protocol failed: {error}")))
    }

    async fn cancel_and_close_bounded(&self) {
        let cancellation = async {
            let mut state = self.inner.lock().await;
            let _ = timeout(
                COMMAND_CONTROL_CANCEL_GRACE / 2,
                write_local_client_frame(&mut state.writer, &LocalClientFrame::Cancel),
            )
            .await;
            let _ = state.writer.shutdown().await;
        };
        let _ = timeout(COMMAND_CONTROL_CANCEL_GRACE, cancellation).await;
    }
}

pub async fn run_fetch<I, O, E>(
    cli: FetchCli,
    mut stdin: I,
    mut stdout: O,
    mut stderr: E,
) -> Result<(), FetchError>
where
    I: AsyncRead + Unpin,
    O: AsyncWrite + Unpin,
    E: AsyncWrite + Unpin,
{
    #[cfg(not(target_os = "linux"))]
    {
        let _ = (&cli, &mut stdin, &mut stdout, &mut stderr);
        return Err(FetchError::new(
            FetchErrorKind::NetworkProtocol,
            "fetch requires Linux Unix-domain descriptor passing and is unavailable on this platform",
        ));
    }
    #[cfg(target_os = "linux")]
    {
        run_linux(cli, &mut stdin, &mut stdout, &mut stderr).await
    }
}

#[cfg(target_os = "linux")]
async fn run_linux<I, O, E>(
    cli: FetchCli,
    stdin: &mut I,
    stdout: &mut O,
    stderr: &mut E,
) -> Result<(), FetchError>
where
    I: AsyncRead + Unpin,
    O: AsyncWrite + Unpin,
    E: AsyncWrite + Unpin,
{
    let body = PreparedBody::prepare(cli.body.clone())?;
    let packet = session::control_packet(&cli, body.declared_len())?;
    let total_timeout = cli.timeout.unwrap_or(Duration::from_secs(30));
    let started = Instant::now();
    let control = control::open_control_fd()?;
    let stream = control::create_and_transfer_session(control, &packet)?;
    let (mut reader, writer) = stream.into_split();
    let writer = Arc::new(SharedWriter::new(writer));
    let deadline = started + total_timeout;
    let mut operation = Box::pin(session::execute(
        &cli,
        body,
        stdin,
        stdout,
        stderr,
        &mut reader,
        Arc::clone(&writer),
    ));
    let result = tokio::select! {
        biased;
        _ = sleep_until(deadline) => Err(timed_out()),
        signal = tokio::signal::ctrl_c() => {
            let _ = signal;
            Err(FetchError::new(FetchErrorKind::BrokenPipe, "fetch canceled"))
        }
        result = operation.as_mut() => result,
    };
    let cancel = matches!(
        result.as_ref().err().map(FetchError::kind),
        Some(
            FetchErrorKind::Timeout
                | FetchErrorKind::BrokenPipe
                | FetchErrorKind::Policy
                | FetchErrorKind::OutputIo
        )
    );
    drop(operation);
    if cancel {
        writer.cancel_and_close_bounded().await;
    }
    drop(writer);
    result
}

#[cfg(target_os = "linux")]
fn state_error(error: impl std::fmt::Display) -> FetchError {
    network(format!("fetch protocol state failed: {error}"))
}

#[cfg(target_os = "linux")]
fn network(message: impl Into<String>) -> FetchError {
    FetchError::new(FetchErrorKind::NetworkProtocol, message)
}

#[cfg(target_os = "linux")]
fn timed_out() -> FetchError {
    FetchError::new(FetchErrorKind::Timeout, "fetch timed out")
}
