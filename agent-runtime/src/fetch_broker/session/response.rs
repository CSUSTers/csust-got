use super::super::{BrokerState, stream::forward_response_body};
use super::{
    attempt::FetchedResponse,
    outcome::{AuditProgress, CompletedRequest, Failure, stream_failure, timeout_remaining},
};
use crate::{
    fetch_policy::BudgetTracker,
    fetch_protocol::{
        BrokerFrame, ClientFrame, FETCH_PROTOCOL_VERSION, FetchResponseHead, read_client_frame,
        write_broker_frame,
    },
};
use http::header;
use std::time::Instant;
use tokio::io::{AsyncRead, AsyncWrite};

pub(super) async fn forward_response<R, C, A, Reader, Writer>(
    state: &BrokerState<R, C, A>,
    reader: &mut Reader,
    writer: &mut Writer,
    response: FetchedResponse,
    deadline: Instant,
    audit_progress: &AuditProgress,
) -> Result<CompletedRequest, Failure>
where
    Reader: AsyncRead + Unpin,
    Writer: AsyncWrite + Unpin,
{
    let reviewed_headers =
        review_response_headers(state, &response.response, response.max_response)?;
    write_broker_frame(
        writer,
        &BrokerFrame::ResponseHead(FetchResponseHead {
            protocol_version: FETCH_PROTOCOL_VERSION,
            status: response.response.status.as_u16(),
            reason: response.response.reason.clone(),
            headers: reviewed_headers.forwarded,
        }),
    )
    .await
    .map_err(super::outcome::protocol_failure)?;
    let forward = forward_response_body(
        response.response.body,
        reviewed_headers.encoding.as_deref(),
        reviewed_headers.budgets,
        audit_progress.response_progress(),
        writer,
    );
    tokio::pin!(forward);
    let streamed = timeout_remaining(deadline, async {
        tokio::select! {
            result = &mut forward => result.map_err(stream_failure),
            frame = read_client_frame(reader) => match frame {
                Ok(ClientFrame::Cancel) => Err(Failure::Canceled(crate::audit::AuditCancellationReason::ClientCancel)),
                Ok(_) => Err(Failure::Protocol),
                Err(_) => Err(Failure::Canceled(crate::audit::AuditCancellationReason::ClientDisconnect)),
            },
        }
    })
    .await??;
    Ok(CompletedRequest {
        decoded_bytes: streamed.1,
        request_bytes: 0,
    })
}

fn review_response_headers<R, C, A>(
    state: &BrokerState<R, C, A>,
    response: &super::super::UpstreamResponse,
    max_response: u64,
) -> Result<ReviewedResponseHeaders, Failure> {
    let mut policy = state.config.policy_config();
    policy.response_decoded_bytes = policy.response_decoded_bytes.min(max_response);
    let mut budgets = BudgetTracker::new(policy);
    let mut headers = Vec::new();
    let mut encoding = None;
    for (name, value) in &response.headers {
        let value = value.to_str().map_err(|_| Failure::Network)?;
        budgets
            .record_response_header(name.as_str(), value)
            .map_err(|_| Failure::Policy)?;
        if name == header::CONTENT_ENCODING {
            encoding = Some(value.to_ascii_lowercase());
        }
        if matches!(
            name.as_str(),
            "set-cookie"
                | "content-encoding"
                | "content-length"
                | "transfer-encoding"
                | "connection"
        ) {
            continue;
        }
        headers.push((name.as_str().to_string(), value.to_string()));
    }
    Ok(ReviewedResponseHeaders {
        forwarded: headers,
        encoding,
        budgets,
    })
}

struct ReviewedResponseHeaders {
    forwarded: Vec<(String, String)>,
    encoding: Option<String>,
    budgets: BudgetTracker,
}
