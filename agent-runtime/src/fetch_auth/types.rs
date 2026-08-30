use serde::{Deserialize, Serialize};
use std::{
    fmt,
    time::{Duration, SystemTime, UNIX_EPOCH},
};

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct FetchClaims {
    pub protocol_version: u16,
    pub policy_version: String,
    pub namespace: String,
    pub run_id: String,
    pub command_id: String,
    pub issued_at_unix: i64,
    pub expires_at_unix: i64,
    pub max_concurrency: u16,
    pub max_requests: u16,
    pub max_request_bytes: u64,
    pub max_response_bytes: u64,
}

#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub struct CommandIdentity {
    pub namespace: String,
    pub run_id: String,
    pub command_id: String,
}

impl CommandIdentity {
    pub fn new(
        namespace: impl Into<String>,
        run_id: impl Into<String>,
        command_id: impl Into<String>,
    ) -> Self {
        Self {
            namespace: namespace.into(),
            run_id: run_id.into(),
            command_id: command_id.into(),
        }
    }

    pub(super) fn from_claims(claims: &FetchClaims) -> Self {
        Self::new(
            claims.namespace.clone(),
            claims.run_id.clone(),
            claims.command_id.clone(),
        )
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct BrokerAuthCaps {
    pub protocol_version: u16,
    pub policy_version: String,
    pub max_concurrency: u16,
    pub max_requests: u16,
    pub max_request_bytes: u64,
    pub max_response_bytes: u64,
    pub max_future_iat: Duration,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct EffectiveLimits {
    pub max_concurrency: u16,
    pub max_requests: u16,
    pub max_request_bytes: u64,
    pub max_response_bytes: u64,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct VerifiedClaims {
    pub claims: FetchClaims,
    pub effective_limits: EffectiveLimits,
    pub identity: CommandIdentity,
}

impl VerifiedClaims {
    pub fn claims(&self) -> &FetchClaims {
        &self.claims
    }

    pub fn effective_limits(&self) -> &EffectiveLimits {
        &self.effective_limits
    }

    pub fn identity(&self) -> &CommandIdentity {
        &self.identity
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum AuthErrorKind {
    InvalidSigningKey,
    MalformedToken,
    InvalidSignature,
    ClockBeforeUnixEpoch,
    Expired,
    IssuedInFuture,
    ProtocolVersionMismatch,
    PolicyVersionMismatch,
    IdentityMismatch,
    ClaimsExceedBrokerCaps,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct AuthError {
    kind: AuthErrorKind,
}

impl AuthError {
    pub(super) fn new(kind: AuthErrorKind) -> Self {
        Self { kind }
    }

    pub fn kind(&self) -> AuthErrorKind {
        self.kind
    }
}

impl fmt::Display for AuthError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(match self.kind {
            AuthErrorKind::InvalidSigningKey => "fetch token signing key is invalid",
            AuthErrorKind::MalformedToken => "fetch token is malformed",
            AuthErrorKind::InvalidSignature => "fetch token signature is invalid",
            AuthErrorKind::ClockBeforeUnixEpoch => "system clock is before the Unix epoch",
            AuthErrorKind::Expired => "fetch token has expired",
            AuthErrorKind::IssuedInFuture => "fetch token was issued too far in the future",
            AuthErrorKind::ProtocolVersionMismatch => "fetch token protocol version does not match",
            AuthErrorKind::PolicyVersionMismatch => "fetch token policy version does not match",
            AuthErrorKind::IdentityMismatch => "fetch token command identity does not match",
            AuthErrorKind::ClaimsExceedBrokerCaps => "fetch token claims exceed broker caps",
        })
    }
}

impl std::error::Error for AuthError {}

pub(super) fn unix_seconds(time: SystemTime) -> Result<i64, AuthError> {
    let seconds = time
        .duration_since(UNIX_EPOCH)
        .map_err(|_| AuthError::new(AuthErrorKind::ClockBeforeUnixEpoch))?
        .as_secs();
    i64::try_from(seconds).map_err(|_| AuthError::new(AuthErrorKind::ClockBeforeUnixEpoch))
}

pub(super) fn duration_seconds(duration: Duration) -> i64 {
    i64::try_from(duration.as_secs()).unwrap_or(i64::MAX)
}
