use super::{
    CommandBindingPhase, CommandControlPacket, LocalRequestState, RuntimeFetchProxyError,
    control::ReceivedControlPacket,
    registry::BindingContext,
    terminal::{LocalTerminalWriter, SessionTaskReceipt, SessionTaskResult, TerminalDelivery},
};
use crate::{
    exec::BashHealth,
    fetch_protocol::{
        AuthMetadata, BrokerFrame, BrokerHello, ClientFrame, ClientHello, FETCH_PROTOCOL_VERSION,
        LocalClientFrame, LocalRuntimeFrame, ProtocolError, read_broker_frame,
        read_local_client_frame, write_client_frame,
    },
    runtime_security::IssuedFetchCommand,
    workspace_budget::WorkspaceBudget,
};
use std::sync::{Arc, Mutex};
use tokio::net::unix::{OwnedReadHalf, OwnedWriteHalf};
use tokio_util::sync::CancellationToken;

pub(super) async fn serve_local_session(
    packet: ReceivedControlPacket,
    context: Arc<BindingContext>,
    cancel: CancellationToken,
) -> SessionTaskResult {
    use std::os::fd::IntoRawFd as _;
    use std::os::unix::io::FromRawFd as _;
    let raw = packet.stream.into_raw_fd();
    let standard = unsafe { std::os::unix::net::UnixStream::from_raw_fd(raw) };
    if standard.set_nonblocking(true).is_err() {
        return Ok(SessionTaskReceipt {
            terminal: TerminalDelivery::Unavailable,
        });
    }
    let local = match tokio::net::UnixStream::from_std(standard) {
        Ok(local) => local,
        Err(_) => {
            return Ok(SessionTaskReceipt {
                terminal: TerminalDelivery::Unavailable,
            });
        }
    };
    let (local_reader, local_writer) = local.into_split();
    let mut terminal = LocalTerminalWriter::new(local_writer);
    let result = run_session(
        local_reader,
        &mut terminal,
        packet.metadata,
        Arc::clone(&context),
        cancel,
    )
    .await;
    match result {
        Ok(receipt) => Ok(receipt),
        Err(error) => Ok(terminal.send_proxy_error(&error).await),
    }
}

async fn run_session(
    mut local_reader: OwnedReadHalf,
    terminal: &mut LocalTerminalWriter<OwnedWriteHalf>,
    metadata: CommandControlPacket,
    context: Arc<BindingContext>,
    cancel: CancellationToken,
) -> SessionTaskResult {
    let Some(socket_path) = &context.broker_socket else {
        return Err(RuntimeFetchProxyError::with_category(
            super::RuntimeFetchProxyErrorCategory::Auth,
            "fetch is unavailable",
        ));
    };
    let issued = context.issued.as_ref().ok_or_else(|| {
        RuntimeFetchProxyError::with_category(
            super::RuntimeFetchProxyErrorCategory::Auth,
            "fetch credential is unavailable",
        )
    })?;
    let broker = tokio::select! {
        biased;
        _ = cancel.cancelled() => {
            return Err(RuntimeFetchProxyError::network("local session was revoked"));
        }
        result = tokio::net::UnixStream::connect(socket_path) => {
            result.map_err(|_| RuntimeFetchProxyError::network("broker connection failed"))?
        },
    };
    proxy_session(
        &mut local_reader,
        terminal,
        broker,
        metadata,
        issued,
        Arc::clone(&context.phase),
        &context.namespace_key,
        &context.workspace_budget,
        context.health.clone(),
        cancel,
    )
    .await
}

#[allow(clippy::too_many_arguments)]
async fn proxy_session(
    local_reader: &mut OwnedReadHalf,
    terminal: &mut LocalTerminalWriter<OwnedWriteHalf>,
    broker: tokio::net::UnixStream,
    metadata: CommandControlPacket,
    issued: &IssuedFetchCommand,
    phase: Arc<Mutex<CommandBindingPhase>>,
    namespace_key: &str,
    budget: &WorkspaceBudget,
    health: BashHealth,
    cancel: CancellationToken,
) -> SessionTaskResult {
    let (mut broker_reader, mut broker_writer) = broker.into_split();
    write_client_frame(
        &mut broker_writer,
        &ClientFrame::Hello(ClientHello {
            protocol_version: FETCH_PROTOCOL_VERSION,
        }),
    )
    .await
    .map_err(protocol_error)?;
    match read_broker_frame(&mut broker_reader)
        .await
        .map_err(protocol_error)?
    {
        BrokerFrame::Hello(BrokerHello { .. }) => {}
        BrokerFrame::Error(error) => {
            return Ok(terminal
                .send_terminal(&LocalRuntimeFrame::Error(error))
                .await);
        }
        _ => {
            return Err(super::state_error(
                "broker Hello is missing or out of order",
            ));
        }
    }
    write_client_frame(
        &mut broker_writer,
        &ClientFrame::Auth(AuthMetadata {
            protocol_version: FETCH_PROTOCOL_VERSION,
            token: issued.token.clone(),
        }),
    )
    .await
    .map_err(protocol_error)?;
    match read_broker_frame(&mut broker_reader)
        .await
        .map_err(protocol_error)?
    {
        BrokerFrame::Authenticated => {}
        BrokerFrame::Error(error) => {
            return Ok(terminal
                .send_terminal(&LocalRuntimeFrame::Error(error))
                .await);
        }
        _ => {
            return Err(super::state_error(
                "broker authentication is missing or out of order",
            ));
        }
    }
    let check_status = metadata.request.check_status;
    write_client_frame(&mut broker_writer, &ClientFrame::Request(metadata.request))
        .await
        .map_err(protocol_error)?;

    let continue_frame = tokio::select! {
        biased;
        _ = cancel.cancelled() => {
            return Err(RuntimeFetchProxyError::network("local session was revoked"));
        }
        early = read_local_client_frame(local_reader) => {
            let _ = early;
            let _ = write_client_frame(&mut broker_writer, &ClientFrame::Cancel).await;
            return Err(super::state_error("local body arrived before Broker Continue"));
        }
        frame = read_broker_frame(&mut broker_reader) => frame.map_err(protocol_error)?,
    };
    match continue_frame {
        BrokerFrame::Continue => {}
        BrokerFrame::Error(error) => {
            return Ok(terminal
                .send_terminal(&LocalRuntimeFrame::Error(error))
                .await);
        }
        _ => {
            return Err(super::state_error(
                "broker Continue is missing or out of order",
            ));
        }
    }
    terminal
        .send_nonterminal(&LocalRuntimeFrame::Continue)
        .await?;
    let mut request_state = LocalRequestState::default();
    request_state.continued()?;
    loop {
        let frame = tokio::select! {
            biased;
            _ = cancel.cancelled() => {
                let _ = write_client_frame(&mut broker_writer, &ClientFrame::Cancel).await;
                return Err(RuntimeFetchProxyError::network("local session was revoked"));
            }
            frame = read_local_client_frame(local_reader) => frame.map_err(protocol_error)?,
        };
        match frame {
            LocalClientFrame::BodyChunk(bytes) => {
                request_state.body_chunk(bytes.len())?;
                write_client_frame(&mut broker_writer, &ClientFrame::BodyChunk(bytes))
                    .await
                    .map_err(protocol_error)?;
            }
            LocalClientFrame::BodyEnd => {
                request_state.body_end()?;
                write_client_frame(&mut broker_writer, &ClientFrame::BodyEnd)
                    .await
                    .map_err(protocol_error)?;
                break;
            }
            LocalClientFrame::Cancel => {
                request_state.cancel()?;
                let _ = write_client_frame(&mut broker_writer, &ClientFrame::Cancel).await;
                return Err(RuntimeFetchProxyError::network(
                    "local session was canceled",
                ));
            }
        }
    }
    super::response::relay_response(
        local_reader,
        terminal,
        broker_reader,
        broker_writer,
        metadata.output_path,
        check_status,
        phase,
        namespace_key,
        budget,
        health,
        cancel,
    )
    .await
}

pub(super) fn protocol_error(error: ProtocolError) -> RuntimeFetchProxyError {
    RuntimeFetchProxyError::protocol(error.to_string())
}
