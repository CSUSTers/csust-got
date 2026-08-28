use super::SharedWriter;
use crate::{
    fetch_cli::{
        FetchCli, FetchError, FetchErrorKind, body::PreparedBody,
        workspace_io::normalize_output_path,
    },
    fetch_protocol::{
        ErrorCode, FETCH_PROTOCOL_VERSION, FetchProtocolErrorFrame, FetchRequestHead,
        LocalRuntimeFrame, ProtocolError, read_local_runtime_frame,
    },
    runtime_fetch_proxy::{CommandControlPacket, LocalResponseState},
};
use std::{io, sync::Arc};
use tokio::{
    io::{AsyncRead, AsyncWrite, AsyncWriteExt as _},
    net::unix::OwnedReadHalf,
};

pub(super) fn control_packet(
    cli: &FetchCli,
    declared_body_bytes: Option<u64>,
) -> Result<CommandControlPacket, FetchError> {
    let request = FetchRequestHead {
        protocol_version: FETCH_PROTOCOL_VERSION,
        method: cli.method.as_str().to_string(),
        url: cli.url.clone(),
        headers: cli
            .headers
            .iter()
            .map(|(name, value)| {
                Ok((
                    name.as_str().to_string(),
                    value
                        .to_str()
                        .map_err(|_| network("request header is not visible ASCII"))?
                        .to_string(),
                ))
            })
            .collect::<Result<_, FetchError>>()?,
        follow: cli.follow,
        check_status: cli.check_status,
        timeout_ms: cli.timeout.map(|duration| duration.as_millis() as u64),
        declared_body_bytes,
    };
    let output_path = cli
        .output
        .as_deref()
        .map(normalize_output_path)
        .transpose()?;
    Ok(CommandControlPacket {
        protocol_version: FETCH_PROTOCOL_VERSION,
        request,
        output_path,
    })
}

#[allow(clippy::too_many_arguments)]
pub(super) async fn execute<I, O, E>(
    cli: &FetchCli,
    body: PreparedBody,
    stdin: &mut I,
    stdout: &mut O,
    stderr: &mut E,
    reader: &mut OwnedReadHalf,
    writer: Arc<SharedWriter>,
) -> Result<(), FetchError>
where
    I: AsyncRead + Unpin,
    O: AsyncWrite + Unpin,
    E: AsyncWrite + Unpin,
{
    match read_local_runtime_frame(reader).await.map_err(protocol)? {
        LocalRuntimeFrame::Continue => writer.continued().await?,
        LocalRuntimeFrame::Error(error) => return Err(server_error(error)),
        _ => return Err(network("runtime did not accept request metadata")),
    }
    body.send(stdin, &writer).await?;

    let mut response_state = LocalResponseState::default();
    let head = match read_local_runtime_frame(reader).await.map_err(protocol)? {
        LocalRuntimeFrame::ResponseHead(head) => {
            response_state.response_head().map_err(state_error)?;
            head
        }
        LocalRuntimeFrame::Error(error) => {
            response_state.error().map_err(state_error)?;
            return Err(server_error(error));
        }
        LocalRuntimeFrame::ResponseChunk(chunk) => {
            response_state
                .response_chunk(chunk.len())
                .map_err(state_error)?;
            return Err(network(
                "runtime sent response bytes before the response head",
            ));
        }
        LocalRuntimeFrame::ResponseEnd(_) => {
            response_state.response_end().map_err(state_error)?;
            return Err(network(
                "runtime ended the response before the response head",
            ));
        }
        LocalRuntimeFrame::Continue => return Err(network("runtime sent a duplicate Continue")),
    };
    if cli.show_headers {
        stderr
            .write_all(format!("HTTP {} {}\n", head.status, head.reason).as_bytes())
            .await
            .map_err(output_io)?;
        for (name, value) in &head.headers {
            stderr
                .write_all(format!("{name}: {value}\n").as_bytes())
                .await
                .map_err(output_io)?;
        }
        stderr.write_all(b"\n").await.map_err(output_io)?;
    }

    let runtime_output = cli.output.is_some();
    let expected_output_commit = runtime_output && !(cli.check_status && head.status >= 400);
    receive_body(
        reader,
        stdout,
        runtime_output,
        expected_output_commit,
        &mut response_state,
    )
    .await?;
    if cli.output.is_none() {
        stdout.flush().await.map_err(output_io)?;
    }
    if cli.check_status && head.status >= 400 {
        return Err(FetchError::new(
            FetchErrorKind::Status,
            format!("HTTP status {} {}", head.status, head.reason),
        ));
    }
    Ok(())
}

async fn receive_body<O: AsyncWrite + Unpin>(
    reader: &mut OwnedReadHalf,
    stdout: &mut O,
    runtime_output: bool,
    expected_output_commit: bool,
    state: &mut LocalResponseState,
) -> Result<(), FetchError> {
    let mut received = 0_u64;
    loop {
        match read_local_runtime_frame(reader).await.map_err(protocol)? {
            LocalRuntimeFrame::ResponseChunk(chunk) => {
                state.response_chunk(chunk.len()).map_err(state_error)?;
                if runtime_output {
                    return Err(network(
                        "runtime sent response body bytes for a committed output",
                    ));
                }
                received = received
                    .checked_add(chunk.len() as u64)
                    .ok_or_else(|| network("response length overflow"))?;
                if let Err(error) = stdout.write_all(&chunk).await {
                    if error.kind() == io::ErrorKind::BrokenPipe {
                        return Err(FetchError::new(
                            FetchErrorKind::BrokenPipe,
                            "downstream pipe closed",
                        ));
                    }
                    return Err(output_io(error));
                }
            }
            LocalRuntimeFrame::ResponseEnd(end) => {
                state.response_end().map_err(state_error)?;
                if end.output_committed != expected_output_commit {
                    return Err(network(
                        "runtime output commit result does not match request metadata",
                    ));
                }
                if !runtime_output && end.body_bytes != received {
                    return Err(network(
                        "runtime response length does not match streamed body",
                    ));
                }
                return Ok(());
            }
            LocalRuntimeFrame::Error(error) => {
                state.error().map_err(state_error)?;
                return Err(server_error(error));
            }
            LocalRuntimeFrame::ResponseHead(_) => {
                state.response_head().map_err(state_error)?;
                return Err(network("runtime sent a duplicate response head"));
            }
            LocalRuntimeFrame::Continue => {
                return Err(network("runtime sent a duplicate Continue"));
            }
        }
    }
}

fn server_error(error: FetchProtocolErrorFrame) -> FetchError {
    let kind = match error.code {
        ErrorCode::Auth => FetchErrorKind::Auth,
        ErrorCode::Policy => FetchErrorKind::Policy,
        ErrorCode::Timeout => FetchErrorKind::Timeout,
        ErrorCode::Network | ErrorCode::Protocol => FetchErrorKind::NetworkProtocol,
        ErrorCode::Internal => FetchErrorKind::OutputIo,
    };
    FetchError::new(
        kind,
        format!("fetch runtime rejected request: {}", error.message),
    )
}

#[cfg(feature = "c7-test-support")]
pub(in crate::fetch_cli) fn c7_server_error(error: FetchProtocolErrorFrame) -> FetchError {
    server_error(error)
}

fn protocol(error: ProtocolError) -> FetchError {
    network(format!("fetch protocol failed: {error}"))
}

fn state_error(error: impl std::fmt::Display) -> FetchError {
    network(format!("fetch protocol state failed: {error}"))
}

fn network(message: impl Into<String>) -> FetchError {
    FetchError::new(FetchErrorKind::NetworkProtocol, message)
}

fn output_io(error: io::Error) -> FetchError {
    FetchError::new(
        FetchErrorKind::OutputIo,
        format!("output I/O failed: {error}"),
    )
}
