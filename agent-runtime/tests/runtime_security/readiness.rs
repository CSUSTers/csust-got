#[cfg(target_os = "linux")]
use agent_runtime::fetch_broker::FetchBroker;
use agent_runtime::{
    audit::{
        AuditCompletion, AuditError, AuditFuture, AuditHealth, AuditSink, AuditStart,
        AuditTransaction,
    },
    fetch_broker::{
        BodyStream, BrokerConfig, BrokerState, ConnectError, PeerCred, PinnedConnector,
        ResolveError, Resolver, ReviewedRequest, UpstreamResponse, serve_connection,
    },
    fetch_policy::ApprovedTarget,
    runtime_security::RuntimeFetchSecurity,
};
use async_trait::async_trait;
use std::{
    net::{IpAddr, SocketAddr},
    path::PathBuf,
    sync::Arc,
    time::Duration,
};

const KEY: &[u8] = b"a sufficiently long runtime security test key";
const OTHER_KEY: &[u8] = b"a different sufficiently long security key";
const PEER: PeerCred = PeerCred {
    uid: 10001,
    gid: 10001,
};

#[tokio::test]
async fn broker_probe_requires_peer_credentials_hmac_policy_and_healthy_audit() {
    let protocol = agent_runtime::fetch_protocol::FETCH_PROTOCOL_VERSION;
    assert!(probe(PEER, KEY, protocol, "policy-v1", AuditHealth::Healthy).await);
    assert!(
        !probe(
            PeerCred {
                uid: 10002,
                gid: PEER.gid
            },
            KEY,
            protocol,
            "policy-v1",
            AuditHealth::Healthy
        )
        .await
    );
    assert!(!probe(PEER, OTHER_KEY, protocol, "policy-v1", AuditHealth::Healthy).await);
    assert!(!probe(PEER, KEY, protocol, "policy-v2", AuditHealth::Healthy).await);
    assert!(!probe(PEER, KEY, protocol + 1, "policy-v1", AuditHealth::Healthy).await);
    assert!(!probe(PEER, KEY, protocol, "policy-v1", AuditHealth::Unhealthy).await);
}

#[cfg(target_os = "linux")]
#[tokio::test]
async fn runtime_probe_uses_the_real_unix_peer_credential_path() {
    let directory = tempfile::tempdir().unwrap();
    let socket = directory.path().join("broker.sock");
    let mut config = broker_config();
    config.socket_path = socket.clone();
    config.peer_uid = unsafe { libc::geteuid() };
    config.peer_gid = unsafe { libc::getegid() };
    let broker = FetchBroker::with_components(
        config,
        KEY,
        NoopResolver,
        NoopConnector,
        FixedAudit(AuditHealth::Healthy),
    )
    .unwrap();
    let server = tokio::spawn(async move { broker.serve().await });
    for _ in 0..100 {
        if socket.exists() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(5)).await;
    }
    assert!(socket.exists(), "broker did not bind its Unix socket");

    let security = RuntimeFetchSecurity::new(
        &socket,
        KEY,
        agent_runtime::config::FetchClaimLimits {
            protocol_version: agent_runtime::fetch_protocol::FETCH_PROTOCOL_VERSION,
            policy_version: "policy-v1".to_string(),
            max_concurrency: 2,
            max_requests: 20,
            max_request_bytes: 8 * 1024 * 1024,
            max_response_bytes: 32 * 1024 * 1024,
        },
    )
    .unwrap();
    assert!(security.probe().await);
    server.abort();
    assert!(server.await.unwrap_err().is_cancelled());
}

async fn probe(
    peer: PeerCred,
    runtime_key: &[u8],
    runtime_protocol: u16,
    runtime_policy: &str,
    audit_health: AuditHealth,
) -> bool {
    let state = Arc::new(
        BrokerState::new(
            broker_config(),
            KEY,
            NoopResolver,
            NoopConnector,
            FixedAudit(audit_health),
        )
        .unwrap(),
    );
    let (mut client, server) = tokio::io::duplex(16 * 1024);
    let task = tokio::spawn(async move { serve_connection(server, peer, state).await });
    let security = RuntimeFetchSecurity::new(
        "/run/agent-fetch/broker.sock",
        runtime_key,
        agent_runtime::config::FetchClaimLimits {
            protocol_version: runtime_protocol,
            policy_version: runtime_policy.to_string(),
            max_concurrency: 2,
            max_requests: 20,
            max_request_bytes: 8 * 1024 * 1024,
            max_response_bytes: 32 * 1024 * 1024,
        },
    )
    .unwrap();
    let ready = security.probe_stream(&mut client).await.is_ok();
    drop(client);
    task.await.unwrap().unwrap();
    ready
}

fn broker_config() -> BrokerConfig {
    BrokerConfig {
        socket_path: PathBuf::from("unused.sock"),
        peer_uid: PEER.uid,
        peer_gid: PEER.gid,
        hmac_key_file: PathBuf::from("unused.key"),
        deny_cidrs: vec!["127.0.0.0/8".parse().unwrap(), "::1/128".parse().unwrap()],
        dns_servers: vec![SocketAddr::from(([1, 1, 1, 1], 53))],
        request_header_max_bytes: 32 * 1024,
        request_body_max_bytes: 8 * 1024 * 1024,
        response_header_max_bytes: 32 * 1024,
        response_network_max_bytes: 16 * 1024 * 1024,
        response_decoded_max_bytes: 32 * 1024 * 1024,
        max_decompression_ratio: 20,
        dns_timeout: Duration::from_millis(100),
        connect_timeout: Duration::from_millis(100),
        first_byte_timeout: Duration::from_millis(100),
        total_timeout: Duration::from_millis(500),
        pre_auth_connections: 64,
        handshake_timeout: Duration::from_secs(2),
        max_concurrency: 2,
        max_requests: 20,
        max_redirects: 5,
        audit_path: PathBuf::from("unused.jsonl"),
        policy_version: "policy-v1".to_string(),
    }
}

struct FixedAudit(AuditHealth);

impl AuditSink for FixedAudit {
    fn begin(&self, _start: AuditStart) -> AuditFuture<'_, AuditTransaction> {
        Box::pin(async { Err(AuditError::Unhealthy) })
    }

    fn complete(
        &self,
        _transaction: AuditTransaction,
        _completion: AuditCompletion,
    ) -> AuditFuture<'_, ()> {
        Box::pin(async { Err(AuditError::Unhealthy) })
    }

    fn health(&self) -> AuditHealth {
        self.0
    }
}

struct NoopResolver;

#[async_trait]
impl Resolver for NoopResolver {
    async fn resolve_all(&self, _host: &str) -> Result<Vec<IpAddr>, ResolveError> {
        Err(ResolveError::Failed)
    }
}

struct NoopConnector;

#[async_trait]
impl PinnedConnector for NoopConnector {
    async fn execute(
        &self,
        _request: ReviewedRequest,
        _target: ApprovedTarget,
        _body: BodyStream,
    ) -> Result<UpstreamResponse, ConnectError> {
        Err(ConnectError::Failed)
    }
}
