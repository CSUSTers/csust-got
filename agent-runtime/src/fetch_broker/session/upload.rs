use super::super::{BrokerState, PinnedConnector, Resolver, transport::body_channel};
use super::{
    attempt::{AttemptResult, attempt},
    outcome::{Failure, timeout_remaining},
    request::{RequestBodyDigest, SessionRequest, digest_record},
};
use crate::{
    audit::{AuditCancellationReason, AuditSink},
    fetch_protocol::{ClientFrame, read_client_frame},
};
use std::{
    sync::{Arc, Mutex},
    time::Instant,
};
use tokio::io::AsyncRead;

pub(super) struct UploadFinished {
    pub(super) bytes: u64,
}

pub(super) async fn stream_initial<R, C, A, Reader>(
    state: Arc<BrokerState<R, C, A>>,
    reader: &mut Reader,
    request: &SessionRequest,
    deadline: Instant,
    digest: Arc<Mutex<RequestBodyDigest>>,
    audit_progress: super::outcome::AuditProgress,
) -> Result<(AttemptResult, UploadFinished), Failure>
where
    R: Resolver + 'static,
    C: PinnedConnector + 'static,
    A: AuditSink + 'static,
    Reader: AsyncRead + Unpin,
{
    let (sender, body) = body_channel(Arc::clone(&state.metrics.body_queue));
    let attempt_state = Arc::clone(&state);
    let attempt_request = request.request.clone();
    let mut attempt = Some(tokio::spawn(async move {
        attempt(
            &attempt_state,
            &attempt_request,
            body,
            deadline,
            &audit_progress,
        )
        .await
    }));
    let mut attempt_result = None;
    let mut bytes = 0_u64;
    loop {
        let frame_result = if let Some(handle) = attempt.as_mut() {
            tokio::select! {
                result = handle => {
                    attempt_result = Some(join_attempt(result));
                    attempt = None;
                    if !matches!(attempt_result, Some(Ok(_))) && request.declared_body_bytes != Some(0) {
                        return Err(attempt_result
                            .expect("attempt result is set")
                            .expect_err("attempt did not succeed"));
                    }
                    continue;
                }
                frame = timeout_remaining(deadline, read_client_frame(reader)) => match frame {
                    Ok(frame) => frame,
                    Err(failure) => {
                        abort_attempt(&mut attempt);
                        return Err(failure);
                    }
                },
            }
        } else {
            match timeout_remaining(deadline, read_client_frame(reader)).await {
                Ok(frame) => frame,
                Err(failure) => return Err(failure),
            }
        };
        let frame = match frame_result {
            Ok(frame) => frame,
            Err(_) => {
                abort_attempt(&mut attempt);
                return Err(Failure::Canceled(AuditCancellationReason::ClientDisconnect));
            }
        };
        match frame {
            ClientFrame::BodyChunk(chunk) => {
                state
                    .metrics
                    .body_frames_read
                    .fetch_add(1, std::sync::atomic::Ordering::Relaxed);
                bytes = bytes
                    .checked_add(chunk.len() as u64)
                    .ok_or(Failure::Policy)?;
                if bytes > request.max_body
                    || request
                        .declared_body_bytes
                        .is_some_and(|declared| bytes > declared)
                {
                    abort_attempt(&mut attempt);
                    return Err(Failure::Policy);
                }
                digest_record(&digest, &chunk)?;
                match timeout_remaining(deadline, sender.send(chunk)).await {
                    Ok(Ok(())) => {}
                    Ok(Err(())) => {
                        abort_attempt(&mut attempt);
                        return Err(attempt_result
                            .and_then(Result::err)
                            .unwrap_or(Failure::Network));
                    }
                    Err(failure) => {
                        abort_attempt(&mut attempt);
                        return Err(failure);
                    }
                }
            }
            ClientFrame::BodyEnd
                if request
                    .declared_body_bytes
                    .is_none_or(|declared| declared == bytes) =>
            {
                drop(sender);
                break;
            }
            ClientFrame::BodyEnd => {
                abort_attempt(&mut attempt);
                return Err(Failure::Policy);
            }
            ClientFrame::Cancel => {
                abort_attempt(&mut attempt);
                return Err(Failure::Canceled(AuditCancellationReason::ClientCancel));
            }
            _ => {
                abort_attempt(&mut attempt);
                return Err(Failure::Protocol);
            }
        }
    }
    let attempt_result = match attempt_result {
        Some(result) => result,
        None => wait_attempt(reader, attempt.expect("attempt is pending"), deadline).await,
    };
    Ok((attempt_result, UploadFinished { bytes }))
}

fn join_attempt(result: Result<AttemptResult, tokio::task::JoinError>) -> AttemptResult {
    result.map_err(|_| Failure::Network)?
}

fn abort_attempt(attempt: &mut Option<tokio::task::JoinHandle<AttemptResult>>) {
    if let Some(handle) = attempt.take() {
        handle.abort();
    }
}

async fn wait_attempt<Reader>(
    reader: &mut Reader,
    mut attempt: tokio::task::JoinHandle<AttemptResult>,
    deadline: Instant,
) -> AttemptResult
where
    Reader: AsyncRead + Unpin,
{
    tokio::select! {
        result = &mut attempt => join_attempt(result),
        frame = timeout_remaining(deadline, read_client_frame(reader)) => {
            attempt.abort();
            match frame {
                Ok(frame) => super::attempt::cancel_frame(frame),
                Err(failure) => Err(failure),
            }
        }
    }
}
