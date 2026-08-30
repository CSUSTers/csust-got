use crate::{
    config::{
        ConfigError, bounded_duration, bounded_number, load_signing_key, required_absolute_path,
        required_list, required_positive_number, required_string,
    },
    fetch_auth::BrokerAuthCaps,
    fetch_policy::PolicyConfig,
    fetch_protocol::FETCH_PROTOCOL_VERSION,
};
use ipnet::IpNet;
use std::{net::SocketAddr, path::PathBuf, time::Duration};

const REQUEST_HEADER_MAX: u64 = 32 * 1024;
const REQUEST_BODY_MAX: u64 = 8 * 1024 * 1024;
const RESPONSE_HEADER_MAX: u64 = 32 * 1024;
const RESPONSE_NETWORK_MAX: u64 = 16 * 1024 * 1024;
const RESPONSE_DECODED_MAX: u64 = 32 * 1024 * 1024;
const RATIO_MAX: u64 = 20;
const DNS_TIMEOUT_MAX_MS: u64 = 2_000;
const CONNECT_TIMEOUT_MAX_MS: u64 = 3_000;
const FIRST_BYTE_TIMEOUT_MAX_MS: u64 = 5_000;
const TOTAL_TIMEOUT_MAX_MS: u64 = 30_000;
const PRE_AUTH_CONNECTIONS_MAX: usize = 64;
const HANDSHAKE_TIMEOUT_MAX_MS: u64 = 2_000;
const CONCURRENCY_MAX: u16 = 2;
const REQUESTS_MAX: u16 = 20;
const REDIRECTS_MAX: u8 = 5;

#[derive(Clone, Debug)]
pub struct BrokerConfig {
    pub socket_path: PathBuf,
    pub peer_uid: u32,
    pub peer_gid: u32,
    pub hmac_key_file: PathBuf,
    pub deny_cidrs: Vec<IpNet>,
    pub dns_servers: Vec<SocketAddr>,
    pub request_header_max_bytes: u64,
    pub request_body_max_bytes: u64,
    pub response_header_max_bytes: u64,
    pub response_network_max_bytes: u64,
    pub response_decoded_max_bytes: u64,
    pub max_decompression_ratio: u64,
    pub dns_timeout: Duration,
    pub connect_timeout: Duration,
    pub first_byte_timeout: Duration,
    pub total_timeout: Duration,
    pub pre_auth_connections: usize,
    pub handshake_timeout: Duration,
    pub max_concurrency: u16,
    pub max_requests: u16,
    pub max_redirects: u8,
    pub audit_path: PathBuf,
    pub policy_version: String,
}

impl BrokerConfig {
    pub fn from_env(get: impl Fn(&str) -> Option<String>) -> Result<Self, ConfigError> {
        let socket_path = required_absolute_path(&get, "AGENT_FETCH_SOCKET")?;
        let peer_uid = required_positive_number(&get, "AGENT_FETCH_PEER_UID")?;
        let peer_gid = required_positive_number(&get, "AGENT_FETCH_PEER_GID")?;
        let hmac_key_file = required_absolute_path(&get, "AGENT_FETCH_HMAC_KEY_FILE")?;
        let deny_cidrs = required_list(&get, "AGENT_FETCH_DENY_CIDRS")?
            .into_iter()
            .map(|value| {
                value.parse().map_err(|_| {
                    ConfigError::new("AGENT_FETCH_DENY_CIDRS must contain valid CIDRs")
                })
            })
            .collect::<Result<_, _>>()?;
        let dns_servers = required_list(&get, "AGENT_FETCH_DNS_SERVERS")?
            .into_iter()
            .map(|value| {
                value.parse().map_err(|_| {
                    ConfigError::new("AGENT_FETCH_DNS_SERVERS must contain socket addresses")
                })
            })
            .collect::<Result<_, _>>()?;

        let config = Self {
            socket_path,
            peer_uid,
            peer_gid,
            hmac_key_file,
            deny_cidrs,
            dns_servers,
            request_header_max_bytes: bounded_number(
                &get,
                "AGENT_FETCH_REQUEST_HEADER_MAX_BYTES",
                REQUEST_HEADER_MAX,
                REQUEST_HEADER_MAX,
            )?,
            request_body_max_bytes: bounded_number(
                &get,
                "AGENT_FETCH_REQUEST_BODY_MAX_BYTES",
                REQUEST_BODY_MAX,
                REQUEST_BODY_MAX,
            )?,
            response_header_max_bytes: bounded_number(
                &get,
                "AGENT_FETCH_RESPONSE_HEADER_MAX_BYTES",
                RESPONSE_HEADER_MAX,
                RESPONSE_HEADER_MAX,
            )?,
            response_network_max_bytes: bounded_number(
                &get,
                "AGENT_FETCH_RESPONSE_NETWORK_MAX_BYTES",
                RESPONSE_NETWORK_MAX,
                RESPONSE_NETWORK_MAX,
            )?,
            response_decoded_max_bytes: bounded_number(
                &get,
                "AGENT_FETCH_RESPONSE_DECODED_MAX_BYTES",
                RESPONSE_DECODED_MAX,
                RESPONSE_DECODED_MAX,
            )?,
            max_decompression_ratio: bounded_number(
                &get,
                "AGENT_FETCH_MAX_DECOMPRESSION_RATIO",
                RATIO_MAX,
                RATIO_MAX,
            )?,
            dns_timeout: bounded_duration(&get, "AGENT_FETCH_DNS_TIMEOUT_MS", DNS_TIMEOUT_MAX_MS)?,
            connect_timeout: bounded_duration(
                &get,
                "AGENT_FETCH_CONNECT_TIMEOUT_MS",
                CONNECT_TIMEOUT_MAX_MS,
            )?,
            first_byte_timeout: bounded_duration(
                &get,
                "AGENT_FETCH_FIRST_BYTE_TIMEOUT_MS",
                FIRST_BYTE_TIMEOUT_MAX_MS,
            )?,
            total_timeout: bounded_duration(
                &get,
                "AGENT_FETCH_TOTAL_TIMEOUT_MS",
                TOTAL_TIMEOUT_MAX_MS,
            )?,
            pre_auth_connections: bounded_number(
                &get,
                "AGENT_FETCH_PRE_AUTH_CONNECTIONS",
                PRE_AUTH_CONNECTIONS_MAX,
                PRE_AUTH_CONNECTIONS_MAX,
            )?,
            handshake_timeout: bounded_duration(
                &get,
                "AGENT_FETCH_HANDSHAKE_TIMEOUT_MS",
                HANDSHAKE_TIMEOUT_MAX_MS,
            )?,
            max_concurrency: bounded_number(
                &get,
                "AGENT_FETCH_MAX_CONCURRENCY",
                CONCURRENCY_MAX,
                CONCURRENCY_MAX,
            )?,
            max_requests: bounded_number(
                &get,
                "AGENT_FETCH_MAX_REQUESTS",
                REQUESTS_MAX,
                REQUESTS_MAX,
            )?,
            max_redirects: bounded_number(
                &get,
                "AGENT_FETCH_MAX_REDIRECTS",
                REDIRECTS_MAX,
                REDIRECTS_MAX,
            )?,
            audit_path: required_absolute_path(&get, "AGENT_FETCH_AUDIT_PATH")?,
            policy_version: required_string(&get, "AGENT_FETCH_POLICY_VERSION")?,
        };
        require_enabled(&get)?;
        Ok(config)
    }

    pub fn policy_config(&self) -> PolicyConfig {
        PolicyConfig {
            deny_cidrs: self.deny_cidrs.clone(),
            credential_header_names: Vec::new(),
            request_header_bytes: self.request_header_max_bytes,
            request_body_bytes: self.request_body_max_bytes,
            response_header_bytes: self.response_header_max_bytes,
            response_network_bytes: self.response_network_max_bytes,
            response_decoded_bytes: self.response_decoded_max_bytes,
            max_decompression_ratio: self.max_decompression_ratio,
            max_redirects: self.max_redirects,
        }
    }

    pub fn auth_caps(&self) -> BrokerAuthCaps {
        BrokerAuthCaps {
            protocol_version: FETCH_PROTOCOL_VERSION,
            policy_version: self.policy_version.clone(),
            max_concurrency: self.max_concurrency,
            max_requests: self.max_requests,
            max_request_bytes: self.request_body_max_bytes,
            max_response_bytes: self.response_decoded_max_bytes,
            max_future_iat: Duration::from_secs(5),
        }
    }

    pub fn load_signing_key(&self) -> Result<Vec<u8>, ConfigError> {
        load_signing_key(&self.hmac_key_file, "broker")
    }
}

fn require_enabled(get: &impl Fn(&str) -> Option<String>) -> Result<(), ConfigError> {
    if get("AGENT_FETCH_ENABLED").as_deref() == Some("true") {
        Ok(())
    } else {
        Err(ConfigError::new(
            "AGENT_FETCH_ENABLED is required and must be exactly true",
        ))
    }
}
