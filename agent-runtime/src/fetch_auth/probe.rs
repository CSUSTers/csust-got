use super::{
    crypto::{SigningKey, constant_time_eq},
    types::{AuthError, AuthErrorKind},
};
use crate::fetch_protocol::{FetchProbe, FetchReady};
use std::fmt;

const PROBE_DOMAIN: &[u8] = b"agent-fetch-probe-v1\0";
const READY_DOMAIN: &[u8] = b"agent-fetch-ready-v1\0";

pub struct ProbeAuthenticator {
    signing_key: SigningKey,
}

impl ProbeAuthenticator {
    pub fn new(signing_key: impl AsRef<[u8]>) -> Result<Self, AuthError> {
        Ok(Self {
            signing_key: SigningKey::new(signing_key)?,
        })
    }

    pub fn create_probe(
        &self,
        protocol_version: u16,
        policy_version: &str,
        nonce: [u8; 16],
    ) -> FetchProbe {
        FetchProbe {
            protocol_version,
            policy_version: policy_version.to_string(),
            nonce,
            mac: self.sign(PROBE_DOMAIN, protocol_version, policy_version, &nonce),
        }
    }

    pub fn verify_probe(&self, probe: &FetchProbe) -> Result<(), AuthError> {
        let expected = self.sign(
            PROBE_DOMAIN,
            probe.protocol_version,
            &probe.policy_version,
            &probe.nonce,
        );
        if constant_time_eq(&expected, &probe.mac) {
            Ok(())
        } else {
            Err(AuthError::new(AuthErrorKind::InvalidSignature))
        }
    }

    pub fn create_ready(&self, probe: &FetchProbe) -> FetchReady {
        FetchReady {
            protocol_version: probe.protocol_version,
            policy_version: probe.policy_version.clone(),
            nonce: probe.nonce,
            mac: self.sign(
                READY_DOMAIN,
                probe.protocol_version,
                &probe.policy_version,
                &probe.nonce,
            ),
        }
    }

    pub fn verify_ready(&self, ready: &FetchReady) -> Result<(), AuthError> {
        let expected = self.sign(
            READY_DOMAIN,
            ready.protocol_version,
            &ready.policy_version,
            &ready.nonce,
        );
        if constant_time_eq(&expected, &ready.mac) {
            Ok(())
        } else {
            Err(AuthError::new(AuthErrorKind::InvalidSignature))
        }
    }

    fn sign(
        &self,
        domain: &[u8],
        protocol_version: u16,
        policy_version: &str,
        nonce: &[u8; 16],
    ) -> [u8; 32] {
        let policy = policy_version.as_bytes();
        let mut message = Vec::with_capacity(domain.len() + 2 + 8 + policy.len() + nonce.len());
        message.extend_from_slice(domain);
        message.extend_from_slice(&protocol_version.to_be_bytes());
        message.extend_from_slice(&(policy.len() as u64).to_be_bytes());
        message.extend_from_slice(policy);
        message.extend_from_slice(nonce);
        self.signing_key.sign(&message)
    }
}

impl fmt::Debug for ProbeAuthenticator {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str("ProbeAuthenticator([REDACTED])")
    }
}
