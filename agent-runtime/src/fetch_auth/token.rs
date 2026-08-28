use super::{
    crypto::{
        SigningKey, base64url_decode, base64url_encode, canonical_base64url_decode,
        constant_time_eq,
    },
    types::{
        AuthError, AuthErrorKind, BrokerAuthCaps, CommandIdentity, FetchClaims, VerifiedClaims,
        duration_seconds, unix_seconds,
    },
};
use crate::fetch_protocol::SecretString;
use std::{fmt, time::SystemTime};

const HEADER: &[u8] = br#"{"alg":"HS256","typ":"JWT"}"#;

pub struct TokenIssuer {
    signing_key: SigningKey,
}

impl TokenIssuer {
    pub fn new(signing_key: impl AsRef<[u8]>) -> Result<Self, AuthError> {
        Ok(Self {
            signing_key: SigningKey::new(signing_key)?,
        })
    }

    pub fn issue(&self, claims: &FetchClaims) -> Result<SecretString, AuthError> {
        let header = base64url_encode(HEADER);
        let claims = serde_json::to_vec(claims)
            .map_err(|_| AuthError::new(AuthErrorKind::MalformedToken))?;
        let claims = base64url_encode(&claims);
        let signing_input = format!("{header}.{claims}");
        let signature = base64url_encode(&self.signing_key.sign(signing_input.as_bytes()));
        Ok(SecretString::new(format!("{signing_input}.{signature}")))
    }
}

impl fmt::Debug for TokenIssuer {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str("TokenIssuer([REDACTED])")
    }
}

pub struct TokenVerifier {
    signing_key: SigningKey,
    caps: BrokerAuthCaps,
}

impl TokenVerifier {
    pub fn new(signing_key: impl AsRef<[u8]>, caps: BrokerAuthCaps) -> Result<Self, AuthError> {
        Ok(Self {
            signing_key: SigningKey::new(signing_key)?,
            caps,
        })
    }

    pub fn verify(
        &self,
        token: &SecretString,
        now: SystemTime,
    ) -> Result<VerifiedClaims, AuthError> {
        let (encoded_header, encoded_claims, encoded_signature) = split_token(token)?;
        let signing_input = format!("{encoded_header}.{encoded_claims}");
        let expected_signature = self.signing_key.sign(signing_input.as_bytes());
        let supplied_signature = base64url_decode(encoded_signature)
            .ok_or_else(|| AuthError::new(AuthErrorKind::InvalidSignature))?;
        if !constant_time_eq(&expected_signature, &supplied_signature) {
            return Err(AuthError::new(AuthErrorKind::InvalidSignature));
        }

        let header = canonical_base64url_decode(encoded_header)?;
        if header != HEADER {
            return Err(AuthError::new(AuthErrorKind::MalformedToken));
        }
        let claims = canonical_base64url_decode(encoded_claims)?;
        let claims: FetchClaims = serde_json::from_slice(&claims)
            .map_err(|_| AuthError::new(AuthErrorKind::MalformedToken))?;

        self.validate_claims(claims, now)
    }

    pub fn verify_for(
        &self,
        token: &SecretString,
        now: SystemTime,
        expected_identity: &CommandIdentity,
    ) -> Result<VerifiedClaims, AuthError> {
        let verified = self.verify(token, now)?;
        if &verified.identity != expected_identity {
            return Err(AuthError::new(AuthErrorKind::IdentityMismatch));
        }
        Ok(verified)
    }

    fn validate_claims(
        &self,
        claims: FetchClaims,
        now: SystemTime,
    ) -> Result<VerifiedClaims, AuthError> {
        let now = unix_seconds(now)?;
        if claims.expires_at_unix <= now {
            return Err(AuthError::new(AuthErrorKind::Expired));
        }
        let latest_issued_at = now.saturating_add(duration_seconds(self.caps.max_future_iat));
        if claims.issued_at_unix > latest_issued_at {
            return Err(AuthError::new(AuthErrorKind::IssuedInFuture));
        }
        if claims.protocol_version != self.caps.protocol_version {
            return Err(AuthError::new(AuthErrorKind::ProtocolVersionMismatch));
        }
        if claims.policy_version != self.caps.policy_version {
            return Err(AuthError::new(AuthErrorKind::PolicyVersionMismatch));
        }
        if claims.max_concurrency > self.caps.max_concurrency
            || claims.max_requests > self.caps.max_requests
            || claims.max_request_bytes > self.caps.max_request_bytes
            || claims.max_response_bytes > self.caps.max_response_bytes
        {
            return Err(AuthError::new(AuthErrorKind::ClaimsExceedBrokerCaps));
        }

        Ok(VerifiedClaims {
            identity: CommandIdentity::from_claims(&claims),
            effective_limits: super::types::EffectiveLimits {
                max_concurrency: claims.max_concurrency.min(self.caps.max_concurrency),
                max_requests: claims.max_requests.min(self.caps.max_requests),
                max_request_bytes: claims.max_request_bytes.min(self.caps.max_request_bytes),
                max_response_bytes: claims.max_response_bytes.min(self.caps.max_response_bytes),
            },
            claims,
        })
    }
}

impl fmt::Debug for TokenVerifier {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter
            .debug_struct("TokenVerifier")
            .field("signing_key", &"[REDACTED]")
            .field("caps", &self.caps)
            .finish()
    }
}

fn split_token(token: &SecretString) -> Result<(&str, &str, &str), AuthError> {
    let mut parts = token.expose_secret().split('.');
    let header = parts
        .next()
        .filter(|part| !part.is_empty())
        .ok_or_else(|| AuthError::new(AuthErrorKind::MalformedToken))?;
    let claims = parts
        .next()
        .filter(|part| !part.is_empty())
        .ok_or_else(|| AuthError::new(AuthErrorKind::MalformedToken))?;
    let signature = parts
        .next()
        .filter(|part| !part.is_empty())
        .ok_or_else(|| AuthError::new(AuthErrorKind::MalformedToken))?;
    if parts.next().is_some() {
        return Err(AuthError::new(AuthErrorKind::MalformedToken));
    }
    Ok((header, claims, signature))
}
