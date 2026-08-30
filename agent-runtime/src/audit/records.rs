use super::redaction::{redacted_header_name, redacted_origin, sha256_hex};
use serde::Serialize;
use sha2::{Digest, Sha256};
use std::{net::IpAddr, time::Duration};

#[derive(Clone, Debug)]
pub struct AuditStart {
    pub(super) identity: AuditIdentity,
    pub(super) request: AuditRequest,
}

impl AuditStart {
    #[allow(clippy::too_many_arguments)]
    pub fn new(
        namespace: &str,
        run_id: &str,
        command_id: &str,
        method: impl Into<String>,
        normalized_origin: &str,
        query: &[u8],
        request_body: &[u8],
        sensitive_headers: impl IntoIterator<Item = AuditSensitiveHeader>,
        policy_version: impl Into<String>,
    ) -> Self {
        Self {
            identity: AuditIdentity {
                namespace_sha256: sha256_hex(namespace.as_bytes()),
                run_id_sha256: sha256_hex(run_id.as_bytes()),
                command_id_sha256: sha256_hex(command_id.as_bytes()),
            },
            request: AuditRequest {
                method: method.into(),
                normalized_origin: redacted_origin(normalized_origin),
                query_byte_len: query.len() as u64,
                query_sha256: sha256_hex(query),
                request_body_byte_len: request_body.len() as u64,
                request_body_sha256: sha256_hex(request_body),
                sensitive_headers: sensitive_headers.into_iter().collect(),
                policy_version: policy_version.into(),
            },
        }
    }
}

#[derive(Clone, Debug)]
pub struct AuditTransaction {
    pub(super) identity: AuditIdentity,
    pub(super) request: AuditRequest,
}

impl AuditTransaction {
    pub fn namespace_sha256(&self) -> &str {
        &self.identity.namespace_sha256
    }

    pub fn run_id_sha256(&self) -> &str {
        &self.identity.run_id_sha256
    }

    pub fn command_id_sha256(&self) -> &str {
        &self.identity.command_id_sha256
    }
}

#[derive(Clone, Debug, Serialize)]
pub struct AuditSensitiveHeader {
    name: String,
    byte_len: u64,
    sha256: String,
}

impl AuditSensitiveHeader {
    pub fn new(name: &str, value: &[u8]) -> Self {
        Self {
            name: redacted_header_name(name),
            byte_len: value.len() as u64,
            sha256: sha256_hex(value),
        }
    }
}

#[derive(Clone, Debug)]
pub struct AuditCompletion {
    pub status: Option<u16>,
    pub approved_ip: Option<IpAddr>,
    pub redirect_chain: Vec<AuditRedirect>,
    pub network_bytes: u64,
    pub decoded_bytes: u64,
    pub request_body_bytes: u64,
    pub request_body_sha256: String,
    pub duration: Duration,
    pub quota: AuditQuotaUse,
    pub rejection_reason: Option<AuditRejectionReason>,
    pub cancellation_reason: Option<AuditCancellationReason>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct AuditBodyDigest {
    byte_len: u64,
    sha256: String,
}

impl AuditBodyDigest {
    pub fn empty() -> Self {
        Self::from_sha256(0, Sha256::digest([]))
    }

    pub fn from_sha256(byte_len: u64, digest: impl AsRef<[u8]>) -> Self {
        Self {
            byte_len,
            sha256: digest
                .as_ref()
                .iter()
                .map(|byte| format!("{byte:02x}"))
                .collect(),
        }
    }

    pub fn byte_len(&self) -> u64 {
        self.byte_len
    }

    pub fn sha256(&self) -> &str {
        &self.sha256
    }
}

impl AuditCompletion {
    #[allow(clippy::too_many_arguments)]
    pub fn new(
        status: Option<u16>,
        approved_ip: Option<IpAddr>,
        redirect_chain: Vec<AuditRedirect>,
        network_bytes: u64,
        decoded_bytes: u64,
        request_body: AuditBodyDigest,
        duration: Duration,
        quota: AuditQuotaUse,
    ) -> Self {
        Self {
            status,
            approved_ip,
            redirect_chain,
            network_bytes,
            decoded_bytes,
            request_body_bytes: request_body.byte_len,
            request_body_sha256: request_body.sha256,
            duration,
            quota,
            rejection_reason: None,
            cancellation_reason: None,
        }
    }
}

#[derive(Clone, Debug, Serialize)]
pub struct AuditRedirect {
    normalized_origin: String,
    approved_ip: IpAddr,
}

impl AuditRedirect {
    pub fn new(normalized_origin: &str, approved_ip: IpAddr) -> Self {
        Self {
            normalized_origin: redacted_origin(normalized_origin),
            approved_ip,
        }
    }
}

#[derive(Clone, Debug, Default, Serialize)]
pub struct AuditQuotaUse {
    pub requests_used: u64,
    pub concurrent_requests: u64,
    pub request_bytes_used: u64,
    pub response_bytes_used: u64,
}

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum AuditRejectionReason {
    Authentication,
    Policy,
    Protocol,
    Quota,
    Timeout,
    AuditUnavailable,
    Upstream,
}

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum AuditCancellationReason {
    BrokerShutdown,
    BrokenPipe,
    ClientCancel,
    ClientDisconnect,
    Timeout,
}

#[derive(Clone, Debug, Serialize)]
pub(super) struct AuditIdentity {
    namespace_sha256: String,
    run_id_sha256: String,
    command_id_sha256: String,
}

#[derive(Clone, Debug, Serialize)]
pub(super) struct AuditRequest {
    method: String,
    normalized_origin: String,
    query_byte_len: u64,
    query_sha256: String,
    request_body_byte_len: u64,
    request_body_sha256: String,
    sensitive_headers: Vec<AuditSensitiveHeader>,
    policy_version: String,
}

#[derive(Serialize)]
#[serde(tag = "event", rename_all = "snake_case")]
pub(super) enum AuditRecord<'a> {
    Start {
        #[serde(flatten)]
        identity: &'a AuditIdentity,
        #[serde(flatten)]
        request: &'a AuditRequest,
    },
    Completion {
        #[serde(flatten)]
        identity: &'a AuditIdentity,
        #[serde(flatten)]
        request: CompletionAuditRequest<'a>,
        #[serde(flatten)]
        completion: CompletionAuditRecord,
    },
}

#[derive(Serialize)]
pub(super) struct CompletionAuditRequest<'a> {
    method: &'a str,
    normalized_origin: &'a str,
    query_byte_len: u64,
    query_sha256: &'a str,
    request_body_byte_len: u64,
    sensitive_headers: &'a [AuditSensitiveHeader],
    policy_version: &'a str,
}

impl<'a> From<&'a AuditRequest> for CompletionAuditRequest<'a> {
    fn from(request: &'a AuditRequest) -> Self {
        Self {
            method: &request.method,
            normalized_origin: &request.normalized_origin,
            query_byte_len: request.query_byte_len,
            query_sha256: &request.query_sha256,
            request_body_byte_len: request.request_body_byte_len,
            sensitive_headers: &request.sensitive_headers,
            policy_version: &request.policy_version,
        }
    }
}

#[derive(Serialize)]
pub(super) struct CompletionAuditRecord {
    status: Option<u16>,
    approved_ip: Option<IpAddr>,
    redirect_chain: Vec<AuditRedirect>,
    network_bytes: u64,
    decoded_bytes: u64,
    request_body_bytes: u64,
    request_body_sha256: String,
    duration_ms: u128,
    quota: AuditQuotaUse,
    rejection_reason: Option<AuditRejectionReason>,
    cancellation_reason: Option<AuditCancellationReason>,
}

impl From<AuditCompletion> for CompletionAuditRecord {
    fn from(completion: AuditCompletion) -> Self {
        Self {
            status: completion.status,
            approved_ip: completion.approved_ip,
            redirect_chain: completion.redirect_chain,
            network_bytes: completion.network_bytes,
            decoded_bytes: completion.decoded_bytes,
            request_body_bytes: completion.request_body_bytes,
            request_body_sha256: completion.request_body_sha256,
            duration_ms: completion.duration.as_millis(),
            quota: completion.quota,
            rejection_reason: completion.rejection_reason,
            cancellation_reason: completion.cancellation_reason,
        }
    }
}
