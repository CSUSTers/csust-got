use super::super::BrokerState;
use crate::{
    audit::{AuditBodyDigest, AuditSensitiveHeader, AuditStart},
    fetch_auth::VerifiedClaims,
    fetch_broker::transport::ReviewedRequest,
    fetch_protocol::{AuthMetadata, FETCH_PROTOCOL_VERSION, FetchRequestHead},
};
use http::Method;
use sha2::{Digest as _, Sha256};
use std::{
    sync::{Arc, Mutex},
    time::{Duration, SystemTime},
};

use super::outcome::Failure;

pub(super) fn verify_auth<R, C, A>(
    state: &BrokerState<R, C, A>,
    auth: &AuthMetadata,
) -> Result<VerifiedClaims, Failure> {
    if auth.protocol_version != FETCH_PROTOCOL_VERSION {
        return Err(Failure::Protocol);
    }
    state
        .verifier
        .verify(&auth.token, SystemTime::now())
        .map_err(|_| Failure::Auth)
}

pub(super) fn review_request<R, C, A>(
    state: &BrokerState<R, C, A>,
    claims: &VerifiedClaims,
    head: FetchRequestHead,
) -> Result<SessionRequest, Failure> {
    if head.protocol_version != FETCH_PROTOCOL_VERSION {
        return Err(Failure::Protocol);
    }
    let method = Method::from_bytes(head.method.as_bytes()).map_err(|_| Failure::Policy)?;
    if method == Method::CONNECT {
        return Err(Failure::Policy);
    }
    let target = state
        .targets
        .normalize(&head.url)
        .map_err(|_| Failure::Policy)?;
    let headers = state
        .headers
        .review(&head.headers)
        .map_err(|_| Failure::Policy)?;
    let max_body = claims.effective_limits.max_request_bytes;
    if head
        .declared_body_bytes
        .is_some_and(|bytes| bytes > max_body)
    {
        return Err(Failure::Policy);
    }
    Ok(SessionRequest {
        request: ReviewedRequest {
            method,
            target,
            headers,
        },
        follow: head.follow,
        declared_body_bytes: head.declared_body_bytes,
        timeout_ms: head.timeout_ms,
        max_body,
        max_response: claims.effective_limits.max_response_bytes,
    })
}

pub(super) fn audit_start<R, C, A>(
    claims: &VerifiedClaims,
    request: &SessionRequest,
    state: &BrokerState<R, C, A>,
) -> AuditStart {
    let sensitive = request
        .request
        .headers
        .headers
        .iter()
        .filter(|(name, _)| request.request.headers.is_sensitive(name.as_str()))
        .map(|(name, value)| AuditSensitiveHeader::new(name.as_str(), value.as_bytes()));
    AuditStart::new(
        &claims.identity.namespace,
        &claims.identity.run_id,
        &claims.identity.command_id,
        request.request.method.as_str(),
        request.request.target.url.as_str(),
        request
            .request
            .target
            .url
            .query()
            .unwrap_or_default()
            .as_bytes(),
        &[],
        sensitive,
        &state.config.policy_version,
    )
}

pub(super) fn request_timeout<R, C, A>(
    state: &BrokerState<R, C, A>,
    request: &SessionRequest,
) -> Duration {
    request
        .timeout_ms
        .map(Duration::from_millis)
        .unwrap_or(state.config.total_timeout)
        .min(state.config.total_timeout)
}

#[derive(Default)]
pub(super) struct RequestBodyDigest {
    hasher: Sha256,
    bytes: u64,
}

impl RequestBodyDigest {
    pub(super) fn record(&mut self, chunk: &[u8]) -> Result<(), Failure> {
        self.bytes = self
            .bytes
            .checked_add(chunk.len() as u64)
            .ok_or(Failure::Policy)?;
        self.hasher.update(chunk);
        Ok(())
    }

    fn snapshot(&self) -> AuditBodyDigest {
        AuditBodyDigest::from_sha256(self.bytes, self.hasher.clone().finalize())
    }
}

pub(super) fn digest_record(
    digest: &Arc<Mutex<RequestBodyDigest>>,
    chunk: &[u8],
) -> Result<(), Failure> {
    digest.lock().map_err(|_| Failure::Network)?.record(chunk)
}

pub(super) fn digest_snapshot(digest: &Arc<Mutex<RequestBodyDigest>>) -> AuditBodyDigest {
    digest
        .lock()
        .map(|digest| digest.snapshot())
        .unwrap_or_else(|_| AuditBodyDigest::empty())
}

#[derive(Clone)]
pub(super) struct SessionRequest {
    pub(super) request: ReviewedRequest,
    pub(super) follow: bool,
    pub(super) declared_body_bytes: Option<u64>,
    pub(super) timeout_ms: Option<u64>,
    pub(super) max_body: u64,
    pub(super) max_response: u64,
}
