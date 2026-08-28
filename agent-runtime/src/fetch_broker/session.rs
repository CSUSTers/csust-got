mod attempt;
mod handshake;
mod outcome;
mod request;
mod response;
mod upload;

use super::{
    BrokerError, BrokerState, PeerCred, PinnedConnector, Resolver, admission::PreAuthAdmission,
};
use crate::{
    audit::{AuditBodyDigest, AuditCancellationReason, AuditSink},
    fetch_protocol::{
        BrokerFrame, ClientFrame, FETCH_PROTOCOL_VERSION, FetchProtocolErrorFrame,
        FetchResponseEnd, read_client_frame, write_broker_frame,
    },
};
use outcome::{AuditProgress, CompletedRequest, Failure, completion, protocol_io};
use request::{RequestBodyDigest, audit_start, digest_snapshot, request_timeout, review_request};
use std::{
    sync::{Arc, Mutex, atomic::Ordering},
    time::Instant,
};
use tokio::io::{AsyncRead, AsyncWrite};

pub(crate) async fn serve_connection<S, R, C, A>(
    stream: S,
    peer: PeerCred,
    state: Arc<BrokerState<R, C, A>>,
    admission: PreAuthAdmission,
) -> Result<(), BrokerError>
where
    S: AsyncRead + AsyncWrite + Unpin + Send,
    R: Resolver + 'static,
    C: PinnedConnector + 'static,
    A: AuditSink + 'static,
{
    let (mut reader, mut writer) = tokio::io::split(stream);
    let pre_auth = tokio::time::timeout_at(
        admission.deadline(),
        handshake::authenticate(&mut reader, &mut writer, peer, &state),
    )
    .await;
    let outcome = match pre_auth {
        Ok(result) => result?,
        Err(_) => {
            state
                .metrics
                .handshake_timeouts
                .fetch_add(1, Ordering::Relaxed);
            return Ok(());
        }
    };
    drop(admission);
    let handshake::PreAuthOutcome::Authenticated(claims) = outcome else {
        return Ok(());
    };
    let head = match read_client_frame(&mut reader).await {
        Ok(ClientFrame::Request(head)) => head,
        _ => return reject(&mut writer, Failure::Protocol).await,
    };
    let request = match review_request(&state, &claims, head) {
        Ok(request) => request,
        Err(failure) => return reject(&mut writer, failure).await,
    };
    let lease = match state.quotas.acquire(&claims) {
        Ok(lease) => lease,
        Err(_) => return reject(&mut writer, Failure::Auth).await,
    };
    let transaction = match state
        .audit
        .begin(audit_start(&claims, &request, &state))
        .await
    {
        Ok(transaction) => transaction,
        Err(_) => return reject(&mut writer, Failure::Auth).await,
    };
    let started = Instant::now();
    let deadline = started + request_timeout(&state, &request);
    let audit_progress = AuditProgress::new();
    if let Err(error) = write_broker_frame(&mut writer, &BrokerFrame::Continue).await {
        let completion = completion(
            &Err(Failure::Canceled(AuditCancellationReason::BrokenPipe)),
            started.elapsed(),
            &lease,
            &audit_progress,
            AuditBodyDigest::empty(),
        );
        let _ = state.audit.complete(transaction, completion).await;
        return Err(protocol_io(error));
    }
    let digest = Arc::new(Mutex::new(RequestBodyDigest::default()));
    let result = run_request(
        Arc::clone(&state),
        &mut reader,
        &mut writer,
        request,
        deadline,
        Arc::clone(&digest),
        &audit_progress,
    )
    .await;
    let completion = completion(
        &result,
        started.elapsed(),
        &lease,
        &audit_progress,
        digest_snapshot(&digest),
    );
    let completion_result = state.audit.complete(transaction, completion).await;
    drop(lease);
    match (result, completion_result) {
        (Ok(done), Ok(())) => write_broker_frame(
            &mut writer,
            &BrokerFrame::ResponseEnd(FetchResponseEnd {
                protocol_version: FETCH_PROTOCOL_VERSION,
                body_bytes: done.decoded_bytes,
            }),
        )
        .await
        .map_err(protocol_io),
        (Ok(_), Err(_)) => reject(&mut writer, Failure::Auth).await,
        (Err(Failure::Canceled(_)), _) => Ok(()),
        (Err(failure), _) => reject(&mut writer, failure).await,
    }
}

async fn run_request<R, C, A, Reader, Writer>(
    state: Arc<BrokerState<R, C, A>>,
    reader: &mut Reader,
    writer: &mut Writer,
    request: request::SessionRequest,
    deadline: Instant,
    digest: Arc<Mutex<RequestBodyDigest>>,
    audit_progress: &AuditProgress,
) -> Result<CompletedRequest, Failure>
where
    R: Resolver + 'static,
    C: PinnedConnector + 'static,
    A: AuditSink + 'static,
    Reader: AsyncRead + Unpin,
    Writer: AsyncWrite + Unpin,
{
    let (initial_response, upload) = upload::stream_initial(
        Arc::clone(&state),
        reader,
        &request,
        deadline,
        Arc::clone(&digest),
        audit_progress.clone(),
    )
    .await?;
    let (response, approved_ip) = match initial_response {
        Ok(response) => response,
        Err(Failure::Network) if upload.bytes == 0 => {
            attempt::attempt_with_cancel(
                &state,
                reader,
                &request.request,
                super::transport::BodyStream::empty(),
                deadline,
                audit_progress,
            )
            .await?
        }
        Err(failure) => return Err(failure),
    };
    let response = attempt::follow_redirects(
        &state,
        reader,
        &request,
        response,
        approved_ip,
        upload.bytes,
        deadline,
        audit_progress,
    )
    .await?;
    let mut completed =
        response::forward_response(&state, reader, writer, response, deadline, audit_progress)
            .await?;
    completed.request_bytes = upload.bytes;
    Ok(completed)
}

async fn reject<W: AsyncWrite + Unpin>(
    writer: &mut W,
    failure: Failure,
) -> Result<(), BrokerError> {
    write_broker_frame(
        writer,
        &BrokerFrame::Error(FetchProtocolErrorFrame {
            protocol_version: FETCH_PROTOCOL_VERSION,
            code: failure.code(),
            message: failure.message().to_string(),
        }),
    )
    .await
    .map_err(protocol_io)
}
