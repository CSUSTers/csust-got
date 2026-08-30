use super::super::{
    BrokerState, PinnedConnector, Resolver,
    transport::{BodyStream, ConnectError, ReviewedRequest, UpstreamResponse},
};
use super::{
    outcome::{AuditProgress, Failure, remaining, timeout_remaining},
    request::SessionRequest,
};
use crate::{
    audit::AuditRedirect,
    fetch_policy::{BodyReplay, TargetHost},
    fetch_protocol::{ClientFrame, ProtocolError, read_client_frame},
};
use http::{StatusCode, header};
use std::time::Instant;
use tokio::{io::AsyncRead, time::timeout};

pub(super) type AttemptResult = Result<(UpstreamResponse, std::net::IpAddr), Failure>;

pub(super) struct FetchedResponse {
    pub(super) response: UpstreamResponse,
    pub(super) max_response: u64,
}

pub(super) async fn follow_redirects<R, C, A, Reader>(
    state: &BrokerState<R, C, A>,
    reader: &mut Reader,
    request: &SessionRequest,
    response: UpstreamResponse,
    approved_ip: std::net::IpAddr,
    request_bytes: u64,
    deadline: Instant,
    audit_progress: &AuditProgress,
) -> Result<FetchedResponse, Failure>
where
    R: Resolver,
    C: PinnedConnector,
    Reader: AsyncRead + Unpin,
{
    let follow = request.follow;
    let max_response = request.max_response;
    let mut request = request.request.clone();
    let mut response = response;
    let mut approved_ip = approved_ip;
    let mut body = if request_bytes == 0 {
        BodyReplay::Empty
    } else {
        BodyReplay::NonReplayable {
            bytes: Some(request_bytes),
        }
    };
    let mut hops = 0_u8;
    loop {
        if !request_is_redirect(response.status, follow, &response.headers) {
            return Ok(FetchedResponse {
                response,
                max_response,
            });
        }
        let location = response
            .headers
            .get(header::LOCATION)
            .and_then(|value| value.to_str().ok())
            .ok_or(Failure::Policy)?;
        let decision = state
            .redirects
            .review(
                &request.target,
                response.status,
                location,
                request.headers.clone(),
                request.method.clone(),
                body,
                hops,
            )
            .map_err(|_| Failure::Policy)?;
        let origin = request.target.url.origin().ascii_serialization();
        audit_progress.record_redirect(AuditRedirect::new(origin.as_str(), approved_ip));
        hops = decision.hops;
        body = match decision.body {
            BodyReplay::Empty => BodyReplay::Empty,
            BodyReplay::Replayable { bytes: 0 } => BodyReplay::Empty,
            BodyReplay::Replayable { .. } => return Err(Failure::Policy),
            BodyReplay::NonReplayable { .. } => return Err(Failure::Policy),
        };
        request = ReviewedRequest {
            method: decision.method,
            target: decision.target,
            headers: decision.headers,
        };
        (response, approved_ip) = attempt_with_cancel(
            state,
            reader,
            &request,
            BodyStream::empty(),
            deadline,
            audit_progress,
        )
        .await?;
    }
}

pub(super) async fn attempt_with_cancel<R, C, A, Reader>(
    state: &BrokerState<R, C, A>,
    reader: &mut Reader,
    request: &ReviewedRequest,
    body: BodyStream,
    deadline: Instant,
    audit_progress: &AuditProgress,
) -> AttemptResult
where
    R: Resolver,
    C: PinnedConnector,
    Reader: AsyncRead + Unpin,
{
    let attempt = attempt(state, request, body, deadline, audit_progress);
    tokio::pin!(attempt);
    tokio::select! {
        result = &mut attempt => result,
        frame = read_client_frame(reader) => cancel_frame(frame),
    }
}

pub(super) async fn attempt<R, C, A>(
    state: &BrokerState<R, C, A>,
    request: &ReviewedRequest,
    body: BodyStream,
    deadline: Instant,
    audit_progress: &AuditProgress,
) -> AttemptResult
where
    R: Resolver,
    C: PinnedConnector,
{
    let host = match &request.target.host {
        TargetHost::Name(host) => host.clone(),
        TargetHost::Address(address) => address.to_string(),
    };
    state
        .metrics
        .resolver_calls
        .fetch_add(1, std::sync::atomic::Ordering::Relaxed);
    let resolved = timeout_remaining(
        deadline,
        timeout(state.config.dns_timeout, state.resolver.resolve_all(&host)),
    )
    .await?;
    let answers = resolved
        .map_err(|_| Failure::Timeout)?
        .map_err(|error| match error {
            super::super::transport::ResolveError::Timeout => Failure::Timeout,
            super::super::transport::ResolveError::Failed => Failure::Network,
        })?;
    let approved = state
        .targets
        .review_answers(request.target.clone(), &answers)
        .map_err(|_| Failure::Policy)?;
    let approved_ip = approved.addresses[0].ip();
    audit_progress.record_approved_ip(approved_ip);
    state
        .metrics
        .connector_calls
        .fetch_add(1, std::sync::atomic::Ordering::Relaxed);
    let response = timeout(
        remaining(deadline)?.min(state.config.first_byte_timeout),
        state.connector.execute(request.clone(), approved, body),
    )
    .await
    .map_err(|_| Failure::Timeout)?
    .map_err(|error| match error {
        ConnectError::Timeout => Failure::Timeout,
        ConnectError::PeerMismatch | ConnectError::Failed => Failure::Network,
    })?;
    audit_progress.record_head(&response, approved_ip);
    Ok((response, approved_ip))
}

pub(super) fn cancel_frame(frame: Result<ClientFrame, ProtocolError>) -> AttemptResult {
    match frame {
        Ok(ClientFrame::Cancel) => Err(Failure::Canceled(
            crate::audit::AuditCancellationReason::ClientCancel,
        )),
        Ok(_) => Err(Failure::Protocol),
        Err(_) => Err(Failure::Canceled(
            crate::audit::AuditCancellationReason::ClientDisconnect,
        )),
    }
}

fn request_is_redirect(status: StatusCode, follow: bool, headers: &http::HeaderMap) -> bool {
    follow && status.is_redirection() && headers.contains_key(header::LOCATION)
}
