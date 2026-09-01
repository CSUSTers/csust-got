use super::{
    CommandBindingPhase, LocalResponseState, OutputCommitGuard, RuntimeFetchProxyError,
    session::protocol_error,
    state_error,
    terminal::{LocalTerminalWriter, SessionTaskResult},
};
use crate::{
    exec::BashHealth,
    fetch_protocol::{
        BrokerFrame, ClientFrame, LocalClientFrame, LocalResponseEnd, LocalRuntimeFrame,
        read_broker_frame, read_local_client_frame, write_client_frame,
    },
    workspace_budget::WorkspaceBudget,
};
use std::sync::{Arc, Mutex};
use tokio::net::unix::{OwnedReadHalf, OwnedWriteHalf};
use tokio_util::sync::CancellationToken;

#[allow(clippy::too_many_arguments)]
pub(super) async fn relay_response(
    local_reader: &mut OwnedReadHalf,
    terminal: &mut LocalTerminalWriter<OwnedWriteHalf>,
    mut broker_reader: OwnedReadHalf,
    mut broker_writer: OwnedWriteHalf,
    output_path: Option<String>,
    check_status: bool,
    phase: Arc<Mutex<CommandBindingPhase>>,
    namespace_key: &str,
    budget: &WorkspaceBudget,
    context_health: BashHealth,
    cancel: CancellationToken,
) -> SessionTaskResult {
    let mut response_state = LocalResponseState::default();
    let mut output = output_path
        .map(|path| {
            OutputCommitGuard::new_with_health_and_namespace_key(
                budget.root(),
                namespace_key,
                &path,
                budget,
                Arc::clone(&phase),
                context_health,
            )
        })
        .transpose()?;
    let mut response_status = None;
    let mut response_bytes = 0_u64;
    loop {
        let frame = tokio::select! {
            biased;
            _ = cancel.cancelled() => {
                let _ = write_client_frame(&mut broker_writer, &ClientFrame::Cancel).await;
                return Err(RuntimeFetchProxyError::network("local session was revoked"));
            }
            local_frame = read_local_client_frame(local_reader) => {
                match local_frame {
                    Ok(LocalClientFrame::Cancel) => {
                        let _ = write_client_frame(&mut broker_writer, &ClientFrame::Cancel).await;
                        return Err(RuntimeFetchProxyError::network("local session was canceled"));
                    }
                    _ => {
                        let _ = write_client_frame(&mut broker_writer, &ClientFrame::Cancel).await;
                        return Err(state_error("local request frame follows BodyEnd"));
                    }
                }
            }
            frame = read_broker_frame(&mut broker_reader) => frame.map_err(protocol_error)?,
        };
        match frame {
            BrokerFrame::ResponseHead(head) => {
                response_state.response_head()?;
                response_status = Some(head.status);
                terminal
                    .send_nonterminal(&LocalRuntimeFrame::ResponseHead(head))
                    .await?;
            }
            BrokerFrame::ResponseChunk(bytes) => {
                response_state.response_chunk(bytes.len())?;
                response_bytes =
                    response_bytes
                        .checked_add(bytes.len() as u64)
                        .ok_or_else(|| {
                            RuntimeFetchProxyError::new("broker response length overflow")
                        })?;
                if let Some(output) = output.as_mut() {
                    output.write_chunk(&bytes)?;
                } else {
                    terminal
                        .send_nonterminal(&LocalRuntimeFrame::ResponseChunk(bytes))
                        .await?;
                }
            }
            BrokerFrame::ResponseEnd(end) => {
                if end.body_bytes != response_bytes {
                    return Err(state_error(
                        "broker response length does not match streamed body",
                    ));
                }
                let should_commit =
                    !check_status || response_status.is_some_and(|status| status < 400);
                let output_committed = if should_commit && let Some(output) = output.take() {
                    output.commit_if_active()?;
                    true
                } else {
                    false
                };
                response_state.response_end()?;
                return Ok(terminal
                    .send_terminal(&LocalRuntimeFrame::ResponseEnd(LocalResponseEnd {
                        protocol_version: end.protocol_version,
                        body_bytes: end.body_bytes,
                        output_committed,
                    }))
                    .await);
            }
            BrokerFrame::Error(error) => {
                response_state.error()?;
                return Ok(terminal
                    .send_terminal(&LocalRuntimeFrame::Error(error))
                    .await);
            }
            _ => return Err(state_error("broker response frame is out of order")),
        }
    }
}
