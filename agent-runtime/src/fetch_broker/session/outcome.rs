use super::super::{
    BrokerError, UpstreamResponse,
    stream::{ResponseProgress, StreamError},
};
use crate::{
    audit::{
        AuditBodyDigest, AuditCancellationReason, AuditCompletion, AuditQuotaUse, AuditRedirect,
        AuditRejectionReason,
    },
    fetch_protocol::{ErrorCode, ProtocolError},
};
use std::{
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};
use tokio::time::timeout;

#[derive(Clone)]
pub(super) struct AuditProgress {
    context: Arc<Mutex<AuditContext>>,
    response_progress: Arc<ResponseProgress>,
}

#[derive(Default)]
struct AuditContext {
    status: Option<u16>,
    approved_ip: Option<std::net::IpAddr>,
    redirects: Vec<AuditRedirect>,
}

impl AuditProgress {
    pub(super) fn new() -> Self {
        Self {
            context: Arc::new(Mutex::new(AuditContext::default())),
            response_progress: Arc::new(ResponseProgress::default()),
        }
    }

    pub(super) fn record_approved_ip(&self, approved_ip: std::net::IpAddr) {
        self.context
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
            .approved_ip = Some(approved_ip);
    }

    pub(super) fn record_head(&self, response: &UpstreamResponse, approved_ip: std::net::IpAddr) {
        let mut context = self
            .context
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        context.status = Some(response.status.as_u16());
        context.approved_ip = Some(approved_ip);
    }

    pub(super) fn record_redirect(&self, redirect: AuditRedirect) {
        self.context
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
            .redirects
            .push(redirect);
    }

    pub(super) fn response_progress(&self) -> Arc<ResponseProgress> {
        Arc::clone(&self.response_progress)
    }

    fn snapshot(&self) -> AuditProgressSnapshot {
        let context = self
            .context
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        let (network_bytes, decoded_bytes) = self.response_progress.snapshot();
        AuditProgressSnapshot {
            status: context.status,
            approved_ip: context.approved_ip,
            redirects: context.redirects.clone(),
            network_bytes,
            decoded_bytes,
        }
    }
}

struct AuditProgressSnapshot {
    status: Option<u16>,
    approved_ip: Option<std::net::IpAddr>,
    redirects: Vec<AuditRedirect>,
    network_bytes: u64,
    decoded_bytes: u64,
}

pub(super) struct CompletedRequest {
    pub(super) decoded_bytes: u64,
    pub(super) request_bytes: u64,
}

pub(super) fn completion(
    result: &Result<CompletedRequest, Failure>,
    duration: Duration,
    lease: &crate::fetch_auth::QuotaLease,
    audit_progress: &AuditProgress,
    request_body: AuditBodyDigest,
) -> AuditCompletion {
    let snapshot = audit_progress.snapshot();
    match result {
        Ok(done) => AuditCompletion::new(
            snapshot.status,
            snapshot.approved_ip,
            snapshot.redirects,
            snapshot.network_bytes,
            snapshot.decoded_bytes,
            request_body,
            duration,
            AuditQuotaUse {
                requests_used: u64::from(lease.requests_used()),
                concurrent_requests: u64::from(lease.concurrent_requests()),
                request_bytes_used: done.request_bytes,
                response_bytes_used: done.decoded_bytes,
            },
        ),
        Err(failure) => AuditCompletion {
            status: snapshot.status,
            approved_ip: snapshot.approved_ip,
            redirect_chain: snapshot.redirects,
            network_bytes: snapshot.network_bytes,
            decoded_bytes: snapshot.decoded_bytes,
            request_body_bytes: request_body.byte_len(),
            request_body_sha256: request_body.sha256().to_string(),
            duration,
            quota: AuditQuotaUse {
                requests_used: u64::from(lease.requests_used()),
                concurrent_requests: u64::from(lease.concurrent_requests()),
                request_bytes_used: request_body.byte_len(),
                response_bytes_used: snapshot.decoded_bytes,
            },
            rejection_reason: failure.rejection_reason(),
            cancellation_reason: failure.cancellation_reason(),
        },
    }
}

pub(super) async fn timeout_remaining<T>(deadline: Instant, future: T) -> Result<T::Output, Failure>
where
    T: std::future::Future,
{
    timeout(remaining(deadline)?, future)
        .await
        .map_err(|_| Failure::Timeout)
}

pub(super) fn remaining(deadline: Instant) -> Result<Duration, Failure> {
    deadline
        .checked_duration_since(Instant::now())
        .ok_or(Failure::Timeout)
}

pub(super) fn protocol_io(error: ProtocolError) -> BrokerError {
    match error {
        ProtocolError::Io(error) => BrokerError::Io(error),
        _ => BrokerError::Configuration("broker protocol encoding failed"),
    }
}

pub(super) fn protocol_failure(error: ProtocolError) -> Failure {
    match error {
        ProtocolError::Io(error) if response_pipe_closed(error.kind()) => {
            Failure::Canceled(AuditCancellationReason::BrokenPipe)
        }
        _ => Failure::Network,
    }
}

fn response_pipe_closed(kind: std::io::ErrorKind) -> bool {
    matches!(
        kind,
        std::io::ErrorKind::BrokenPipe
            | std::io::ErrorKind::ConnectionReset
            | std::io::ErrorKind::ConnectionAborted
            | std::io::ErrorKind::NotConnected
            | std::io::ErrorKind::UnexpectedEof
    )
}

pub(super) fn stream_failure(error: StreamError) -> Failure {
    match error {
        StreamError::Policy => Failure::Policy,
        StreamError::Upstream => Failure::Network,
        StreamError::Output(error) => protocol_failure(error),
    }
}

#[derive(Clone)]
pub(super) enum Failure {
    Auth,
    Policy,
    Timeout,
    Network,
    Protocol,
    Canceled(AuditCancellationReason),
}

impl Failure {
    pub(super) fn code(&self) -> ErrorCode {
        match self {
            Self::Auth => ErrorCode::Auth,
            Self::Policy => ErrorCode::Policy,
            Self::Timeout => ErrorCode::Timeout,
            Self::Network | Self::Canceled(_) => ErrorCode::Network,
            Self::Protocol => ErrorCode::Protocol,
        }
    }

    pub(super) fn message(&self) -> &'static str {
        match self {
            Self::Auth => "broker authentication or quota rejected the request",
            Self::Policy => "broker policy rejected the request",
            Self::Timeout => "broker request timed out",
            Self::Network => "broker upstream request failed",
            Self::Protocol => "broker protocol rejected the request",
            Self::Canceled(_) => "broker request was canceled",
        }
    }

    fn rejection_reason(&self) -> Option<AuditRejectionReason> {
        match self {
            Self::Auth => Some(AuditRejectionReason::Authentication),
            Self::Policy => Some(AuditRejectionReason::Policy),
            Self::Timeout => Some(AuditRejectionReason::Timeout),
            Self::Network => Some(AuditRejectionReason::Upstream),
            Self::Protocol => Some(AuditRejectionReason::Protocol),
            Self::Canceled(_) => None,
        }
    }

    fn cancellation_reason(&self) -> Option<AuditCancellationReason> {
        match self {
            Self::Canceled(reason) => Some(reason.clone()),
            Self::Timeout => Some(AuditCancellationReason::Timeout),
            _ => None,
        }
    }
}
