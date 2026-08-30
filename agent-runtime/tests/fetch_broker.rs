use agent_runtime::{
    audit::{AuditWriter, JsonlAuditSink},
    config::FetchClaimLimits,
    fetch_auth::{FetchClaims, ProbeAuthenticator, TokenIssuer},
    fetch_broker::{
        BodyStream, BrokerConfig, BrokerState, ConnectError, FetchBroker, PeerCred,
        PinnedConnector, PinnedHttpClient, ResolveError, Resolver, ReviewedRequest,
        UpstreamResponse, serve_connection,
    },
    fetch_protocol::{
        AuthMetadata, BrokerFrame, ClientFrame, ClientHello, ErrorCode, FETCH_PROTOCOL_VERSION,
        FetchRequestHead, SecretString, read_broker_frame, write_client_frame,
    },
    runtime_security::RuntimeFetchSecurity,
};
use async_trait::async_trait;
use bytes::Bytes;
use flate2::{
    Compression,
    write::{GzEncoder, ZlibEncoder},
};
use futures_util::StreamExt as _;
use http::{HeaderMap, HeaderName, HeaderValue, StatusCode};
use serde_json::Value;
use sha2::{Digest as _, Sha256};
use std::{
    collections::{HashMap, VecDeque},
    io::Write as _,
    net::{IpAddr, Ipv4Addr, SocketAddr},
    path::PathBuf,
    sync::{
        Arc, Mutex,
        atomic::{AtomicUsize, Ordering},
    },
    task::{Context, Poll},
    time::{Duration, SystemTime, UNIX_EPOCH},
};
use tokio::{
    io::{AsyncReadExt as _, AsyncWriteExt as _, DuplexStream},
    sync::Notify,
    time::{advance, sleep, timeout},
};

const KEY: &[u8] = b"a sufficiently long broker test signing key";
const PEER: PeerCred = PeerCred { uid: 101, gid: 102 };

#[test]
fn broker_config_requires_enable_security_inputs_and_bounds_optional_numbers() {
    let error = BrokerConfig::from_env(|_| None).unwrap_err();
    assert!(error.to_string().contains("AGENT_FETCH_SOCKET"));

    let mut values = config_env();
    values.insert("AGENT_FETCH_REQUEST_BODY_MAX_BYTES", "0");
    let error =
        BrokerConfig::from_env(|name| values.get(name).map(|value| value.to_string())).unwrap_err();
    assert!(
        error
            .to_string()
            .contains("AGENT_FETCH_REQUEST_BODY_MAX_BYTES")
    );

    let config =
        BrokerConfig::from_env(|name| config_env().get(name).map(|value| value.to_string()))
            .unwrap();
    assert_eq!(config.request_body_max_bytes, 8 * 1024 * 1024);
    assert_eq!(config.response_network_max_bytes, 16 * 1024 * 1024);
    assert_eq!(config.max_redirects, 5);
    assert_eq!(config.pre_auth_connections, 64);
    assert_eq!(config.handshake_timeout, Duration::from_secs(2));
}

#[test]
fn broker_config_pre_auth_bounds_and_enable_fail_closed() {
    for enabled in [None, Some("false"), Some("TRUE"), Some(" true"), Some("")] {
        let mut values = config_env();
        match enabled {
            Some(value) => {
                values.insert("AGENT_FETCH_ENABLED", value);
            }
            None => {
                values.remove("AGENT_FETCH_ENABLED");
            }
        }
        let error = BrokerConfig::from_env(|name| values.get(name).map(|value| value.to_string()))
            .unwrap_err();
        assert!(error.to_string().contains("AGENT_FETCH_ENABLED"));
    }

    for (name, value) in [
        ("AGENT_FETCH_PRE_AUTH_CONNECTIONS", "0"),
        ("AGENT_FETCH_PRE_AUTH_CONNECTIONS", "65"),
        ("AGENT_FETCH_HANDSHAKE_TIMEOUT_MS", "0"),
        ("AGENT_FETCH_HANDSHAKE_TIMEOUT_MS", "2001"),
    ] {
        let mut values = config_env();
        values.insert(name, value);
        let error = BrokerConfig::from_env(|key| values.get(key).map(|entry| entry.to_string()))
            .unwrap_err();
        assert!(error.to_string().contains(name), "{name}={value}: {error}");
    }

    let mut minimums = config_env();
    minimums.insert("AGENT_FETCH_PRE_AUTH_CONNECTIONS", "1");
    minimums.insert("AGENT_FETCH_HANDSHAKE_TIMEOUT_MS", "1");
    let parsed =
        BrokerConfig::from_env(|name| minimums.get(name).map(|value| value.to_string())).unwrap();
    assert_eq!(parsed.pre_auth_connections, 1);
    assert_eq!(parsed.handshake_timeout, Duration::from_millis(1));
}

#[test]
fn broker_config_and_production_types_are_available() {
    let _ = std::any::TypeId::of::<PinnedHttpClient>();
    let _ = std::any::TypeId::of::<FetchBroker>();
}

#[tokio::test]
async fn preauth_connection_limit_rejects_before_spawning_more_tasks() {
    let mut limited = config();
    limited.pre_auth_connections = 2;
    let records = Arc::new(Mutex::new(Vec::new()));
    let resolver = ScriptedResolver::answers(vec![public_ip()]);
    let resolver_calls = Arc::clone(&resolver.calls);
    let connector = ScriptedConnector::successes([response(200, &[], [])]);
    let connector_calls = Arc::clone(&connector.calls);
    let broker = broker_with_config(
        limited,
        resolver,
        connector,
        JsonlAuditSink::with_writer(RecordingWriter::new(Arc::clone(&records))),
    );
    let state = Arc::clone(broker.state());
    let (first, first_task) = start_state(Arc::clone(&state), PEER);
    let (second, second_task) = start_state(Arc::clone(&state), PEER);

    timeout(Duration::from_millis(100), async {
        while broker.metrics().active_pre_auth != 2 {
            tokio::task::yield_now().await;
        }
    })
    .await
    .expect("two silent clients did not consume the pre-auth permits");

    let (mut third, third_task) = start_state(state, PEER);
    let mut byte = [0_u8; 1];
    assert_eq!(
        timeout(Duration::from_millis(100), third.read(&mut byte))
            .await
            .expect("third connection was not closed promptly")
            .unwrap(),
        0
    );
    third_task.await.unwrap().unwrap();

    let metrics = broker.metrics();
    assert_eq!(metrics.active_pre_auth, 2);
    assert_eq!(metrics.rejected_pre_auth, 1);
    assert_eq!(metrics.handshake_timeouts, 0);
    assert_eq!(metrics.body_frames_read, 0);
    assert_eq!(metrics.resolver_calls, 0);
    assert_eq!(metrics.connector_calls, 0);
    assert_eq!(resolver_calls.load(Ordering::Relaxed), 0);
    assert!(connector_calls.lock().unwrap().is_empty());
    assert!(records.lock().unwrap().is_empty());

    drop(first);
    drop(second);
    let _ = timeout(Duration::from_millis(100), first_task).await;
    let _ = timeout(Duration::from_millis(100), second_task).await;
}

#[tokio::test]
async fn silent_peer_is_closed_at_one_absolute_handshake_deadline() {
    let mut limited = config();
    limited.pre_auth_connections = 1;
    limited.handshake_timeout = Duration::from_millis(50);
    let records = Arc::new(Mutex::new(Vec::new()));
    let resolver = ScriptedResolver::answers(vec![public_ip()]);
    let resolver_calls = Arc::clone(&resolver.calls);
    let connector = ScriptedConnector::successes([response(200, &[], [])]);
    let connector_calls = Arc::clone(&connector.calls);
    let broker = broker_with_config(
        limited,
        resolver,
        connector,
        JsonlAuditSink::with_writer(RecordingWriter::new(Arc::clone(&records))),
    );
    let (mut client, task) = start_state(Arc::clone(broker.state()), PEER);

    let encoded_hello = encoded_client_frame(ClientFrame::Hello(hello())).await;
    client.write_all(&encoded_hello[..1]).await.unwrap();
    sleep(Duration::from_millis(5)).await;
    client.write_all(&encoded_hello[1..5]).await.unwrap();
    sleep(Duration::from_millis(5)).await;
    client.write_all(&encoded_hello[5..]).await.unwrap();
    assert!(matches!(
        timeout(Duration::from_millis(20), read_broker_frame(&mut client))
            .await
            .expect("broker did not answer the fragmented hello")
            .unwrap(),
        BrokerFrame::Hello(_)
    ));
    sleep(Duration::from_millis(10)).await;
    let encoded_auth = encoded_client_frame(ClientFrame::Auth(AuthMetadata {
        protocol_version: FETCH_PROTOCOL_VERSION,
        token: valid_token(),
    }))
    .await;
    client.write_all(&encoded_auth[..1]).await.unwrap();

    let mut byte = [0_u8; 1];
    assert_eq!(
        timeout(Duration::from_millis(150), client.read(&mut byte))
            .await
            .expect("silent peer survived the absolute handshake deadline")
            .unwrap(),
        0
    );
    task.await.unwrap().unwrap();

    let metrics = broker.metrics();
    assert_eq!(metrics.active_pre_auth, 0);
    assert_eq!(metrics.rejected_pre_auth, 0);
    assert_eq!(metrics.handshake_timeouts, 1);
    assert_eq!(metrics.body_frames_read, 0);
    assert_eq!(resolver_calls.load(Ordering::Relaxed), 0);
    assert!(connector_calls.lock().unwrap().is_empty());
    assert!(records.lock().unwrap().is_empty());
}

#[tokio::test(start_paused = true)]
async fn authenticated_idle_peer_is_closed_at_original_request_head_deadline_without_side_effects()
{
    let mut limited = config();
    limited.pre_auth_connections = 1;
    limited.handshake_timeout = Duration::from_millis(50);
    let records = Arc::new(Mutex::new(Vec::new()));
    let resolver = ScriptedResolver::answers(vec![public_ip()]);
    let resolver_calls = Arc::clone(&resolver.calls);
    let connector = ScriptedConnector::successes([response(200, &[], [])]);
    let connector_calls = Arc::clone(&connector.calls);
    let broker = broker_with_config(
        limited,
        resolver,
        connector,
        JsonlAuditSink::with_writer(RecordingWriter::new(Arc::clone(&records))),
    );
    let (mut client, task) = start_state(Arc::clone(broker.state()), PEER);
    authenticate_client(&mut client, valid_token()).await;

    advance(Duration::from_millis(49)).await;
    assert_eq!(broker.metrics().handshake_timeouts, 0);
    assert!(!task.is_finished());

    advance(Duration::from_millis(1)).await;
    tokio::task::yield_now().await;
    assert!(
        task.is_finished(),
        "authenticated idle peer survived the original request head deadline"
    );

    let mut byte = [0_u8; 1];
    assert_eq!(client.read(&mut byte).await.unwrap(), 0);
    task.await.unwrap().unwrap();

    let metrics = broker.metrics();
    assert_eq!(metrics.active_pre_auth, 0);
    assert_eq!(metrics.handshake_timeouts, 1);
    assert_eq!(metrics.body_frames_read, 0);
    assert_eq!(metrics.resolver_calls, 0);
    assert_eq!(metrics.connector_calls, 0);
    assert_eq!(resolver_calls.load(Ordering::Relaxed), 0);
    assert!(connector_calls.lock().unwrap().is_empty());
    assert!(records.lock().unwrap().is_empty());
}

#[tokio::test]
async fn preauth_permit_is_released_after_request_head_decoding() {
    let mut limited = config();
    limited.pre_auth_connections = 1;
    let broker = broker_with_config(
        limited,
        ScriptedResolver::answers(vec![public_ip()]),
        ScriptedConnector::successes([response(200, &[], [])]),
        healthy_audit(),
    );
    let state = Arc::clone(broker.state());
    let (mut authenticated, authenticated_task) = start_state(Arc::clone(&state), PEER);
    authenticate_client(&mut authenticated, valid_token()).await;
    assert_eq!(broker.metrics().active_pre_auth, 1);

    let (mut before_request_head, before_request_head_task) = start_state(Arc::clone(&state), PEER);
    let mut byte = [0_u8; 1];
    assert_eq!(
        timeout(
            Duration::from_millis(100),
            before_request_head.read(&mut byte)
        )
        .await
        .expect("authenticated connection released the permit before the request head")
        .unwrap(),
        0
    );
    before_request_head_task.await.unwrap().unwrap();
    assert_eq!(broker.metrics().active_pre_auth, 1);
    assert_eq!(broker.metrics().rejected_pre_auth, 1);

    write_client_frame(
        &mut authenticated,
        &ClientFrame::Request(request_head("GET", "https://public.example/", &[], false)),
    )
    .await
    .unwrap();
    assert!(matches!(
        read_broker_frame(&mut authenticated).await.unwrap(),
        BrokerFrame::Continue
    ));
    assert_eq!(broker.metrics().active_pre_auth, 0);

    let (silent, silent_task) = start_state(state, PEER);
    timeout(Duration::from_millis(100), async {
        while broker.metrics().active_pre_auth != 1 {
            tokio::task::yield_now().await;
        }
    })
    .await
    .expect("request-head decoding did not release the pre-auth permit");
    assert_eq!(broker.metrics().rejected_pre_auth, 1);

    authenticated_task.abort();
    silent_task.abort();
    drop(authenticated);
    drop(silent);
    let _ = authenticated_task.await;
    let _ = silent_task.await;
}

#[tokio::test]
async fn readiness_probe_obeys_handshake_deadline_without_consuming_quota() {
    let mut limited = config();
    limited.pre_auth_connections = 1;
    limited.handshake_timeout = Duration::from_millis(50);
    limited.max_requests = 1;
    let token = runtime_token_for(&limited);
    let records = Arc::new(Mutex::new(Vec::new()));
    let broker = broker_with_config(
        limited,
        ScriptedResolver::answers(vec![public_ip()]),
        ScriptedConnector::successes([response(200, &[], [b"ready".as_slice()])]),
        JsonlAuditSink::with_writer(RecordingWriter::new(Arc::clone(&records))),
    );
    let state = Arc::clone(broker.state());
    let probe_authenticator = ProbeAuthenticator::new(KEY).unwrap();
    let probe = probe_authenticator.create_probe(FETCH_PROTOCOL_VERSION, "policy-v1", [7; 16]);
    let encoded_probe = encoded_client_frame(ClientFrame::Probe(probe.clone())).await;
    let (mut stalled, stalled_task) = start_state(Arc::clone(&state), PEER);
    stalled.write_all(&encoded_probe[..6]).await.unwrap();
    let mut byte = [0_u8; 1];
    assert_eq!(
        timeout(Duration::from_millis(150), stalled.read(&mut byte))
            .await
            .expect("partial readiness probe survived the handshake deadline")
            .unwrap(),
        0
    );
    stalled_task.await.unwrap().unwrap();
    assert_eq!(broker.metrics().handshake_timeouts, 1);
    assert_eq!(broker.metrics().active_pre_auth, 0);
    assert_eq!(broker.metrics().body_frames_read, 0);
    assert!(records.lock().unwrap().is_empty());

    let (mut ready, ready_task) = start_state(Arc::clone(&state), PEER);
    write_client_frame(&mut ready, &ClientFrame::Probe(probe))
        .await
        .unwrap();
    let response = read_broker_frame(&mut ready).await.unwrap();
    assert!(matches!(response, BrokerFrame::Ready(_)));
    ready_task.await.unwrap().unwrap();
    assert_eq!(broker.metrics().active_pre_auth, 0);

    let (mut request, request_task) = start_state(state, PEER);
    begin_request(
        &mut request,
        token,
        "GET",
        "https://public.example/",
        &[],
        false,
    )
    .await;
    write_client_frame(&mut request, &ClientFrame::BodyEnd)
        .await
        .unwrap();
    let (_, body) = read_success(&mut request).await;
    assert_eq!(body, b"ready");
    request_task.await.unwrap().unwrap();
}

#[tokio::test]
async fn broker_rejects_connect_before_audit_body_dns_or_connector() {
    let mut limited = config();
    limited.max_requests = 1;
    let token = runtime_token_for(&limited);
    let records = Arc::new(Mutex::new(Vec::new()));
    let resolver = ScriptedResolver::answers(vec![public_ip()]);
    let resolver_calls = Arc::clone(&resolver.calls);
    let connector = ScriptedConnector::successes([response(200, &[], [])]);
    let connector_calls = Arc::clone(&connector.calls);
    let broker = broker_with_config(
        limited,
        resolver,
        connector,
        JsonlAuditSink::with_writer(RecordingWriter::new(Arc::clone(&records))),
    );
    let state = Arc::clone(broker.state());

    for _ in 0..2 {
        let (mut client, task) = start_state(Arc::clone(&state), PEER);
        authenticate_client(&mut client, token.clone()).await;
        write_client_frame(
            &mut client,
            &ClientFrame::Request(request_head(
                "CONNECT",
                "https://public.example:443/",
                &[],
                false,
            )),
        )
        .await
        .unwrap();
        assert_error(&mut client, ErrorCode::Policy).await;
        task.await.unwrap().unwrap();
    }

    assert!(records.lock().unwrap().is_empty());
    assert_eq!(resolver_calls.load(Ordering::Relaxed), 0);
    assert!(connector_calls.lock().unwrap().is_empty());
    assert_eq!(broker.metrics().body_frames_read, 0);
    assert_eq!(broker.metrics().resolver_calls, 0);
    assert_eq!(broker.metrics().connector_calls, 0);
}

#[tokio::test]
async fn broker_accepts_only_runtime_peer_uid_gid_and_valid_internal_token() {
    let broker = broker(
        ScriptedResolver::answers(vec![public_ip()]),
        ScriptedConnector::successes([response(200, &[], [])]),
        healthy_audit(),
    );
    let state = Arc::clone(broker.state());
    let token = runtime_token_for(&config());

    for peer in [
        PeerCred {
            uid: PEER.uid + 1,
            gid: PEER.gid,
        },
        PeerCred {
            uid: PEER.uid,
            gid: PEER.gid + 1,
        },
    ] {
        let (mut client, task) = start_state(Arc::clone(&state), peer);
        assert_error(&mut client, ErrorCode::Auth).await;
        task.await.unwrap().unwrap();
    }

    let (mut invalid, invalid_task) = start_state(Arc::clone(&state), PEER);
    write_client_frame(&mut invalid, &ClientFrame::Hello(hello()))
        .await
        .unwrap();
    assert!(matches!(
        read_broker_frame(&mut invalid).await.unwrap(),
        BrokerFrame::Hello(_)
    ));
    write_client_frame(
        &mut invalid,
        &ClientFrame::Auth(AuthMetadata {
            protocol_version: FETCH_PROTOCOL_VERSION,
            token: SecretString::new("not-runtime-issued"),
        }),
    )
    .await
    .unwrap();
    assert_error(&mut invalid, ErrorCode::Auth).await;
    invalid_task.await.unwrap().unwrap();

    let (mut valid, valid_task) = start_state(state, PEER);
    authenticate_client(&mut valid, token).await;
    assert_eq!(broker.metrics().active_pre_auth, 1);
    valid_task.abort();
    drop(valid);
    let _ = valid_task.await;
    assert_eq!(broker.metrics().body_frames_read, 0);
    assert_eq!(broker.metrics().resolver_calls, 0);
    assert_eq!(broker.metrics().connector_calls, 0);
}

#[tokio::test]
async fn invalid_token_is_rejected_before_body_or_egress() {
    let resolver = ScriptedResolver::answers(vec![public_ip()]);
    let resolver_calls = Arc::clone(&resolver.calls);
    let connector = ScriptedConnector::successes([response(200, &[], [b"unused".as_slice()])]);
    let connector_calls = Arc::clone(&connector.calls);
    let broker = broker(resolver, connector, healthy_audit());
    let (mut client, task) = start_state(Arc::clone(broker.state()), PEER);

    write_client_frame(&mut client, &ClientFrame::Hello(hello()))
        .await
        .unwrap();
    write_client_frame(
        &mut client,
        &ClientFrame::Auth(AuthMetadata {
            protocol_version: FETCH_PROTOCOL_VERSION,
            token: SecretString::new("not-a-token"),
        }),
    )
    .await
    .unwrap();
    write_client_frame(
        &mut client,
        &ClientFrame::BodyChunk(Bytes::from_static(b"never read")),
    )
    .await
    .unwrap();

    assert!(matches!(
        read_broker_frame(&mut client).await.unwrap(),
        BrokerFrame::Hello(_)
    ));
    assert_error(&mut client, ErrorCode::Auth).await;
    task.await.unwrap().unwrap();
    assert_eq!(resolver_calls.load(Ordering::Relaxed), 0);
    assert!(connector_calls.lock().unwrap().is_empty());
    assert_eq!(broker.metrics().body_frames_read, 0);
    assert_eq!(broker.metrics().resolver_calls, 0);
    assert_eq!(broker.metrics().connector_calls, 0);
}

#[tokio::test]
async fn incompatible_protocol_version_is_rejected_before_egress() {
    let broker = broker(
        ScriptedResolver::answers(vec![public_ip()]),
        ScriptedConnector::successes([response(200, &[], [])]),
        healthy_audit(),
    );
    let (mut client, task) = start_state(Arc::clone(broker.state()), PEER);
    write_client_frame(
        &mut client,
        &ClientFrame::Hello(ClientHello {
            protocol_version: FETCH_PROTOCOL_VERSION + 1,
        }),
    )
    .await
    .unwrap();

    assert_error(&mut client, ErrorCode::Protocol).await;
    task.await.unwrap().unwrap();
    assert_eq!(
        broker.metrics(),
        agent_runtime::fetch_broker::BrokerMetricsSnapshot::default()
    );
}

#[tokio::test]
async fn peer_credential_mismatch_is_rejected_before_body_or_egress() {
    let broker = broker(
        ScriptedResolver::answers(vec![public_ip()]),
        ScriptedConnector::successes([response(200, &[], [])]),
        healthy_audit(),
    );
    let (mut client, task) = start_with_peer(
        Arc::clone(broker.state()),
        PeerCred {
            uid: PEER.uid + 1,
            gid: PEER.gid,
        },
    );

    assert_error(&mut client, ErrorCode::Auth).await;
    task.await.unwrap().unwrap();
    assert_eq!(
        broker.metrics(),
        agent_runtime::fetch_broker::BrokerMetricsSnapshot::default()
    );
}

#[tokio::test]
async fn validated_request_streams_response_and_discards_set_cookie() {
    let resolver = ScriptedResolver::answers(vec![public_ip()]);
    let connector = ScriptedConnector::successes([response(
        200,
        &[("set-cookie", "session=secret"), ("x-safe", "yes")],
        [b"hello ".as_slice(), b"world".as_slice()],
    )]);
    let calls = Arc::clone(&connector.calls);
    let broker = broker(resolver, connector, healthy_audit());
    let (mut client, task) = start(broker);

    begin_request(
        &mut client,
        valid_token(),
        "GET",
        "https://public.example/items",
        &[],
        false,
    )
    .await;
    write_client_frame(&mut client, &ClientFrame::BodyEnd)
        .await
        .unwrap();
    let (head, body) = read_success(&mut client).await;

    assert_eq!(head.status, 200);
    assert!(
        head.headers
            .iter()
            .any(|(name, value)| name == "x-safe" && value == "yes")
    );
    assert!(
        !head
            .headers
            .iter()
            .any(|(name, _)| name.eq_ignore_ascii_case("set-cookie"))
    );
    assert_eq!(body, b"hello world");
    task.await.unwrap().unwrap();
    assert_eq!(calls.lock().unwrap().len(), 1);
}

#[tokio::test]
async fn signed_request_body_cap_stops_egress_after_continue() {
    let connector = ScriptedConnector::successes([response(200, &[], [])]);
    let calls = Arc::clone(&connector.calls);
    let broker = broker(
        ScriptedResolver::answers(vec![public_ip()]),
        connector,
        healthy_audit(),
    );
    let (mut client, task) = start_state(Arc::clone(broker.state()), PEER);
    let mut request = request_head("POST", "https://public.example/", &[], false);
    request.declared_body_bytes = None;

    write_client_frame(&mut client, &ClientFrame::Hello(hello()))
        .await
        .unwrap();
    assert!(matches!(
        read_broker_frame(&mut client).await.unwrap(),
        BrokerFrame::Hello(_)
    ));
    write_client_frame(
        &mut client,
        &ClientFrame::Auth(AuthMetadata {
            protocol_version: FETCH_PROTOCOL_VERSION,
            token: valid_token_with_limits(4, 32 * 1024 * 1024),
        }),
    )
    .await
    .unwrap();
    assert!(matches!(
        read_broker_frame(&mut client).await.unwrap(),
        BrokerFrame::Authenticated
    ));
    write_client_frame(&mut client, &ClientFrame::Request(request))
        .await
        .unwrap();
    assert!(matches!(
        read_broker_frame(&mut client).await.unwrap(),
        BrokerFrame::Continue
    ));
    write_client_frame(
        &mut client,
        &ClientFrame::BodyChunk(Bytes::from_static(b"five!")),
    )
    .await
    .unwrap();
    assert_error(&mut client, ErrorCode::Policy).await;
    task.await.unwrap().unwrap();
    assert_eq!(broker.metrics().body_frames_read, 1);
    assert_eq!(calls.lock().unwrap().len(), 1);
}

#[tokio::test]
async fn streaming_upload_records_incremental_digest_without_plaintext() {
    let records = Arc::new(Mutex::new(Vec::new()));
    let connector = DrainingConnector::new();
    let chunks = Arc::clone(&connector.chunks);
    let broker = broker(
        ScriptedResolver::answers(vec![public_ip()]),
        connector,
        JsonlAuditSink::with_writer(RecordingWriter::new(Arc::clone(&records))),
    );
    let (mut client, task) = start_state(Arc::clone(broker.state()), PEER);
    let mut request = request_head("POST", "https://public.example/upload", &[], false);
    request.declared_body_bytes = Some(5);

    begin_request_head(&mut client, valid_token(), request).await;
    write_client_frame(
        &mut client,
        &ClientFrame::BodyChunk(Bytes::from_static(b"he")),
    )
    .await
    .unwrap();
    write_client_frame(
        &mut client,
        &ClientFrame::BodyChunk(Bytes::from_static(b"llo")),
    )
    .await
    .unwrap();
    write_client_frame(&mut client, &ClientFrame::BodyEnd)
        .await
        .unwrap();
    let (_, body) = read_success(&mut client).await;
    task.await.unwrap().unwrap();

    assert!(body.is_empty());
    assert_eq!(
        *chunks.lock().unwrap(),
        vec![b"he".to_vec(), b"llo".to_vec()]
    );
    let records = records.lock().unwrap();
    assert_eq!(records.len(), 2);
    let start: Value = serde_json::from_slice(&records[0]).unwrap();
    let completion: Value = serde_json::from_slice(&records[1]).unwrap();
    assert_eq!(start["request_body_byte_len"], 0);
    assert_eq!(start["request_body_sha256"], sha256_hex(b""));
    assert_eq!(completion["request_body_bytes"], 5);
    assert_eq!(completion["request_body_sha256"], sha256_hex(b"hello"));
    assert!(
        !records
            .iter()
            .any(|record| record.windows(5).any(|part| part == b"hello"))
    );
}

#[tokio::test]
async fn streaming_upload_is_bounded_and_connector_sees_chunks_before_upload_end() {
    const CHUNK_BYTES: usize = 32 * 1024;
    const CHUNKS: usize = 32;

    let connector = GatedStreamingConnector::new();
    let started = Arc::clone(&connector.started);
    let allow_first = Arc::clone(&connector.allow_first);
    let first_chunk = Arc::clone(&connector.first_chunk);
    let allow_rest = Arc::clone(&connector.allow_rest);
    let observed = Arc::clone(&connector.observed);
    let broker = broker(
        ScriptedResolver::answers(vec![public_ip()]),
        connector,
        healthy_audit(),
    );
    let (mut client, task) = start_state(Arc::clone(broker.state()), PEER);
    let mut request = request_head("POST", "https://public.example/upload", &[], false);
    request.declared_body_bytes = Some((CHUNK_BYTES * CHUNKS) as u64);
    begin_request_head(&mut client, valid_token(), request).await;
    started.notified().await;

    let writer = tokio::spawn(async move {
        for _ in 0..CHUNKS {
            write_client_frame(
                &mut client,
                &ClientFrame::BodyChunk(Bytes::from(vec![b'x'; CHUNK_BYTES])),
            )
            .await
            .unwrap();
        }
        write_client_frame(&mut client, &ClientFrame::BodyEnd)
            .await
            .unwrap();
        client
    });
    tokio::time::timeout(Duration::from_secs(1), async {
        while broker.metrics().max_queued_body_frames < 4 {
            tokio::task::yield_now().await;
        }
    })
    .await
    .expect("upload channel did not reach its bounded capacity");
    assert_eq!(broker.metrics().max_queued_body_frames, 4);
    assert!(!writer.is_finished());

    allow_first.notify_one();
    first_chunk.notified().await;
    assert_eq!(observed.load(Ordering::Relaxed), 1);
    assert!(!writer.is_finished());
    allow_rest.notify_one();
    let mut client = writer.await.unwrap();
    let _ = read_success(&mut client).await;
    task.await.unwrap().unwrap();

    assert_eq!(observed.load(Ordering::Relaxed), CHUNKS);
    assert_eq!(broker.metrics().queued_body_frames, 0);
    assert_eq!(broker.metrics().max_queued_body_frames, 4);
}

#[tokio::test]
async fn exhausted_token_quota_is_rejected_before_body_or_egress() {
    let broker = broker(
        ScriptedResolver::answers(vec![public_ip()]),
        ScriptedConnector::successes([response(200, &[], []), response(200, &[], [])]),
        healthy_audit(),
    );
    let state = Arc::clone(broker.state());
    let token = valid_token_with_claims(2, 1, 8 * 1024 * 1024, 32 * 1024 * 1024);
    let (mut first, first_task) = start_state(Arc::clone(&state), PEER);
    begin_request(
        &mut first,
        token,
        "GET",
        "https://public.example/",
        &[],
        false,
    )
    .await;
    write_client_frame(&mut first, &ClientFrame::BodyEnd)
        .await
        .unwrap();
    let _ = read_success(&mut first).await;
    first_task.await.unwrap().unwrap();

    let (mut second, second_task) = start_state(state, PEER);
    write_client_frame(&mut second, &ClientFrame::Hello(hello()))
        .await
        .unwrap();
    assert!(matches!(
        read_broker_frame(&mut second).await.unwrap(),
        BrokerFrame::Hello(_)
    ));
    write_client_frame(
        &mut second,
        &ClientFrame::Auth(AuthMetadata {
            protocol_version: FETCH_PROTOCOL_VERSION,
            token: valid_token_with_claims(2, 1, 8 * 1024 * 1024, 32 * 1024 * 1024),
        }),
    )
    .await
    .unwrap();
    assert!(matches!(
        read_broker_frame(&mut second).await.unwrap(),
        BrokerFrame::Authenticated
    ));
    write_client_frame(
        &mut second,
        &ClientFrame::Request(request_head("GET", "https://public.example/", &[], false)),
    )
    .await
    .unwrap();
    assert_error(&mut second, ErrorCode::Auth).await;
    second_task.await.unwrap().unwrap();
    assert_eq!(broker.metrics().resolver_calls, 1);
    assert_eq!(broker.metrics().connector_calls, 1);
}

#[tokio::test]
async fn response_header_cap_is_enforced_before_any_response_output() {
    let mut limited = config();
    limited.response_header_max_bytes = 4;
    let broker = broker_with_config(
        limited,
        ScriptedResolver::answers(vec![public_ip()]),
        ScriptedConnector::successes([response(200, &[("x-safe", "too-long")], [])]),
        healthy_audit(),
    );
    let (mut client, task) = start(broker);

    begin_request(
        &mut client,
        valid_token(),
        "GET",
        "https://public.example/",
        &[],
        false,
    )
    .await;
    write_client_frame(&mut client, &ClientFrame::BodyEnd)
        .await
        .unwrap();
    assert_error(&mut client, ErrorCode::Policy).await;
    task.await.unwrap().unwrap();
}

#[tokio::test]
async fn retries_and_redirects_reresolve_and_strip_cross_origin_credentials() {
    let resolver = ScriptedResolver::answers(vec![public_ip()]);
    let resolver_calls = Arc::clone(&resolver.calls);
    let connector = ScriptedConnector::steps([
        Step::Failure(ConnectError::Failed),
        Step::Response(response(
            302,
            &[("location", "https://other.example/final")],
            [],
        )),
        Step::Response(response(200, &[], [b"ok".as_slice()])),
    ]);
    let calls = Arc::clone(&connector.calls);
    let broker = broker(resolver, connector, healthy_audit());
    let (mut client, task) = start(broker);

    begin_request(
        &mut client,
        valid_token(),
        "GET",
        "https://public.example/start",
        &[("authorization", "Bearer cross-origin-secret")],
        true,
    )
    .await;
    write_client_frame(&mut client, &ClientFrame::BodyEnd)
        .await
        .unwrap();
    let (_, body) = read_success(&mut client).await;
    task.await.unwrap().unwrap();

    let calls = calls.lock().unwrap();
    assert_eq!(calls.len(), 3);
    assert!(calls.iter().all(|call| call.approved_ip == public_ip()));
    assert!(calls[0].has_authorization);
    assert!(calls[1].has_authorization);
    assert!(!calls[2].has_authorization);
    assert_eq!(resolver_calls.load(Ordering::Relaxed), 3);
    assert_eq!(body, b"ok");
}

#[tokio::test]
async fn same_origin_redirect_preserves_credentials() {
    let resolver = ScriptedResolver::answers(vec![public_ip()]);
    let connector = ScriptedConnector::steps([
        Step::Response(response(302, &[("location", "/next")], [])),
        Step::Response(response(200, &[], [b"ok".as_slice()])),
    ]);
    let calls = Arc::clone(&connector.calls);
    let broker = broker(resolver, connector, healthy_audit());
    let (mut client, task) = start(broker);

    begin_request(
        &mut client,
        valid_token(),
        "GET",
        "https://public.example/start",
        &[("authorization", "Bearer retained-secret")],
        true,
    )
    .await;
    write_client_frame(&mut client, &ClientFrame::BodyEnd)
        .await
        .unwrap();
    let _ = read_success(&mut client).await;
    task.await.unwrap().unwrap();
    assert!(
        calls
            .lock()
            .unwrap()
            .iter()
            .all(|call| call.has_authorization)
    );
}

#[tokio::test]
async fn mixed_dns_answers_are_denied_without_connecting() {
    let resolver = ScriptedResolver::answers(vec![public_ip(), IpAddr::V4(Ipv4Addr::LOCALHOST)]);
    let connector = ScriptedConnector::successes([response(200, &[], [])]);
    let calls = Arc::clone(&connector.calls);
    let broker = broker_with_config(config(), resolver, connector, healthy_audit());
    let (mut client, task) = start(broker);

    begin_request(
        &mut client,
        valid_token(),
        "GET",
        "https://public.example/",
        &[],
        false,
    )
    .await;
    write_client_frame(&mut client, &ClientFrame::BodyEnd)
        .await
        .unwrap();
    assert_error(&mut client, ErrorCode::Policy).await;
    task.await.unwrap().unwrap();
    assert!(calls.lock().unwrap().is_empty());
}

#[tokio::test]
async fn dns_connect_first_byte_and_total_timeouts_map_to_timeout() {
    timeout_case(
        config(),
        ScriptedResolver::failure(ResolveError::Timeout),
        ScriptedConnector::successes([]),
    )
    .await;

    let mut short = config();
    short.dns_timeout = Duration::from_millis(1);
    let resolver = ScriptedResolver::delayed(Duration::from_millis(20), vec![public_ip()]);
    timeout_case(short, resolver, ScriptedConnector::successes([])).await;

    let mut short = config();
    short.first_byte_timeout = Duration::from_millis(1);
    let resolver = ScriptedResolver::answers(vec![public_ip()]);
    timeout_case(
        short,
        resolver,
        ScriptedConnector::steps([Step::Delayed(
            Duration::from_millis(20),
            Box::new(Step::Response(response(200, &[], []))),
        )]),
    )
    .await;

    let mut total = config();
    total.total_timeout = Duration::from_millis(1);
    total.first_byte_timeout = Duration::from_secs(1);
    let resolver = ScriptedResolver::answers(vec![public_ip()]);
    timeout_case(
        total,
        resolver,
        ScriptedConnector::steps([Step::Delayed(
            Duration::from_millis(20),
            Box::new(Step::Response(response(200, &[], []))),
        )]),
    )
    .await;

    let resolver = ScriptedResolver::answers(vec![public_ip()]);
    timeout_case(
        config(),
        resolver,
        ScriptedConnector::steps([Step::Failure(ConnectError::Timeout)]),
    )
    .await;
}

#[tokio::test]
async fn raw_decoded_and_ratio_response_limits_abort_without_response_end() {
    let mut raw = config();
    raw.response_network_max_bytes = 4;
    response_limit_case(raw, response(200, &[], [b"12345".as_slice()])).await;

    let mut decoded = config();
    decoded.response_decoded_max_bytes = 4;
    response_limit_case(decoded, response(200, &[], [b"12345".as_slice()])).await;

    response_limit_case_with_token(
        config(),
        valid_token_with_limits(8 * 1024 * 1024, 4),
        response(200, &[], [b"12345".as_slice()]),
    )
    .await;

    let mut compressed = Vec::new();
    GzEncoder::new(&mut compressed, Compression::default())
        .write_all(&vec![b'x'; 1024])
        .unwrap();
    let mut ratio = config();
    ratio.max_decompression_ratio = 2;
    response_limit_case(
        ratio,
        response(
            200,
            &[("content-encoding", "gzip")],
            [compressed.as_slice()],
        ),
    )
    .await;
}

#[tokio::test]
async fn response_budget_failure_audits_known_head_and_consumed_network_bytes() {
    let records = Arc::new(Mutex::new(Vec::new()));
    let mut limited = config();
    limited.response_network_max_bytes = 4;
    let broker = broker_with_config(
        limited.clone(),
        ScriptedResolver::answers(vec![public_ip()]),
        ScriptedConnector::successes([response(200, &[], [b"12345".as_slice()])]),
        JsonlAuditSink::with_writer(RecordingWriter::new(Arc::clone(&records))),
    );
    let (mut client, task) = start(broker);

    begin_request(
        &mut client,
        valid_token_for(&limited),
        "GET",
        "https://public.example/",
        &[],
        false,
    )
    .await;
    write_client_frame(&mut client, &ClientFrame::BodyEnd)
        .await
        .unwrap();
    assert!(matches!(
        read_broker_frame(&mut client).await.unwrap(),
        BrokerFrame::ResponseHead(_)
    ));
    assert_error(&mut client, ErrorCode::Policy).await;
    task.await.unwrap().unwrap();

    let records = records.lock().unwrap();
    let completion: Value = serde_json::from_slice(&records[1]).unwrap();
    assert_eq!(completion["status"], 200);
    assert_eq!(completion["approved_ip"], public_ip().to_string());
    assert_eq!(completion["network_bytes"], 5);
    assert_eq!(completion["decoded_bytes"], 0);
    assert_eq!(completion["rejection_reason"], "policy");
}

#[tokio::test]
async fn cancellation_audits_redirect_and_partial_response_progress() {
    let records = Arc::new(Mutex::new(Vec::new()));
    let connector = ScriptedConnector::steps([
        Step::Response(response(
            302,
            &[("location", "https://other.example/final")],
            [],
        )),
        Step::Response(partial_then_pending_response(b"part")),
    ]);
    let broker = broker(
        ScriptedResolver::answers(vec![public_ip()]),
        connector,
        JsonlAuditSink::with_writer(RecordingWriter::new(Arc::clone(&records))),
    );
    let (mut client, task) = start(broker);

    begin_request(
        &mut client,
        valid_token(),
        "GET",
        "https://public.example/start",
        &[],
        true,
    )
    .await;
    write_client_frame(&mut client, &ClientFrame::BodyEnd)
        .await
        .unwrap();
    assert!(matches!(
        read_broker_frame(&mut client).await.unwrap(),
        BrokerFrame::ResponseHead(_)
    ));
    assert!(matches!(
        read_broker_frame(&mut client).await.unwrap(),
        BrokerFrame::ResponseChunk(chunk) if chunk == Bytes::from_static(b"part")
    ));
    write_client_frame(&mut client, &ClientFrame::Cancel)
        .await
        .unwrap();
    task.await.unwrap().unwrap();

    let records = records.lock().unwrap();
    let completion: Value = serde_json::from_slice(&records[1]).unwrap();
    assert_eq!(completion["status"], 200);
    assert_eq!(completion["approved_ip"], public_ip().to_string());
    assert_eq!(completion["network_bytes"], 4);
    assert_eq!(completion["decoded_bytes"], 4);
    assert_eq!(completion["redirect_chain"].as_array().unwrap().len(), 1);
    assert_eq!(
        completion["redirect_chain"][0]["normalized_origin"],
        "https://public.example"
    );
    assert_eq!(completion["cancellation_reason"], "client_cancel");
}

#[tokio::test]
async fn connector_failure_audits_reviewed_ip_without_inventing_status() {
    let records = Arc::new(Mutex::new(Vec::new()));
    let broker = broker(
        ScriptedResolver::answers(vec![public_ip()]),
        ScriptedConnector::steps([Step::Failure(ConnectError::Failed)]),
        JsonlAuditSink::with_writer(RecordingWriter::new(Arc::clone(&records))),
    );
    let (mut client, task) = start(broker);

    begin_request(
        &mut client,
        valid_token(),
        "GET",
        "https://public.example/",
        &[],
        false,
    )
    .await;
    write_client_frame(&mut client, &ClientFrame::BodyEnd)
        .await
        .unwrap();
    assert_error(&mut client, ErrorCode::Network).await;
    task.await.unwrap().unwrap();

    let records = records.lock().unwrap();
    let completion: Value = serde_json::from_slice(&records[1]).unwrap();
    assert_eq!(completion["status"], Value::Null);
    assert_eq!(completion["approved_ip"], public_ip().to_string());
    assert_eq!(completion["network_bytes"], 0);
    assert_eq!(completion["decoded_bytes"], 0);
    assert_eq!(completion["rejection_reason"], "upstream");
}

#[tokio::test]
async fn gzip_brotli_and_zlib_deflate_responses_decode() {
    let plain = b"decoded payload";
    let mut gzip = GzEncoder::new(Vec::new(), Compression::default());
    gzip.write_all(plain).unwrap();
    assert_eq!(
        decoded_response("gzip", gzip.finish().unwrap()).await,
        plain
    );

    let mut zlib = ZlibEncoder::new(Vec::new(), Compression::default());
    zlib.write_all(plain).unwrap();
    assert_eq!(
        decoded_response("deflate", zlib.finish().unwrap()).await,
        plain
    );

    let mut brotli = Vec::new();
    {
        let mut writer = brotli::CompressorWriter::new(&mut brotli, 4096, 5, 22);
        writer.write_all(plain).unwrap();
    }
    assert_eq!(decoded_response("br", brotli).await, plain);
}

#[tokio::test]
async fn audit_begin_and_completion_fail_closed_before_future_egress() {
    let begin_writer = ControlledWriter::failing_on(1);
    let resolver = ScriptedResolver::answers(vec![public_ip()]);
    let connector = ScriptedConnector::successes([response(200, &[], [])]);
    let calls = Arc::clone(&connector.calls);
    let begin_broker = broker(
        resolver,
        connector,
        JsonlAuditSink::with_writer(begin_writer),
    );
    let (mut client, task) = start(begin_broker);
    write_client_frame(&mut client, &ClientFrame::Hello(hello()))
        .await
        .unwrap();
    assert!(matches!(
        read_broker_frame(&mut client).await.unwrap(),
        BrokerFrame::Hello(_)
    ));
    write_client_frame(
        &mut client,
        &ClientFrame::Auth(AuthMetadata {
            protocol_version: FETCH_PROTOCOL_VERSION,
            token: valid_token(),
        }),
    )
    .await
    .unwrap();
    assert!(matches!(
        read_broker_frame(&mut client).await.unwrap(),
        BrokerFrame::Authenticated
    ));
    write_client_frame(
        &mut client,
        &ClientFrame::Request(request_head("GET", "https://public.example/", &[], false)),
    )
    .await
    .unwrap();
    assert_error(&mut client, ErrorCode::Auth).await;
    task.await.unwrap().unwrap();
    assert!(calls.lock().unwrap().is_empty());

    let completion_writer = ControlledWriter::failing_on(2);
    let resolver = ScriptedResolver::answers(vec![public_ip()]);
    let connector = ScriptedConnector::successes([response(200, &[], []), response(200, &[], [])]);
    let calls = Arc::clone(&connector.calls);
    let completion_broker = broker(
        resolver,
        connector,
        JsonlAuditSink::with_writer(completion_writer),
    );
    let completion_state = Arc::clone(completion_broker.state());
    let (mut first, first_task) = start(completion_broker);
    begin_request(
        &mut first,
        valid_token(),
        "GET",
        "https://public.example/",
        &[],
        false,
    )
    .await;
    write_client_frame(&mut first, &ClientFrame::BodyEnd)
        .await
        .unwrap();
    assert!(matches!(
        read_broker_frame(&mut first).await.unwrap(),
        BrokerFrame::ResponseHead(_)
    ));
    assert_error(&mut first, ErrorCode::Auth).await;
    first_task.await.unwrap().unwrap();
    assert_eq!(calls.lock().unwrap().len(), 1);

    let (mut second, second_task) = start_state(completion_state, PEER);
    write_client_frame(&mut second, &ClientFrame::Hello(hello()))
        .await
        .unwrap();
    assert!(matches!(
        read_broker_frame(&mut second).await.unwrap(),
        BrokerFrame::Hello(_)
    ));
    write_client_frame(
        &mut second,
        &ClientFrame::Auth(AuthMetadata {
            protocol_version: FETCH_PROTOCOL_VERSION,
            token: valid_token(),
        }),
    )
    .await
    .unwrap();
    assert!(matches!(
        read_broker_frame(&mut second).await.unwrap(),
        BrokerFrame::Authenticated
    ));
    write_client_frame(
        &mut second,
        &ClientFrame::Request(request_head("GET", "https://public.example/", &[], false)),
    )
    .await
    .unwrap();
    assert_error(&mut second, ErrorCode::Auth).await;
    second_task.await.unwrap().unwrap();
    assert_eq!(calls.lock().unwrap().len(), 1);
}

#[tokio::test]
async fn client_cancel_aborts_pending_connector_and_completes_without_output() {
    let resolver = ScriptedResolver::answers(vec![public_ip()]);
    let connector = ScriptedConnector::steps([Step::Delayed(
        Duration::from_secs(1),
        Box::new(Step::Response(response(200, &[], []))),
    )]);
    let broker = broker(resolver, connector, healthy_audit());
    let (mut client, task) = start(broker);
    begin_request(
        &mut client,
        valid_token(),
        "GET",
        "https://public.example/",
        &[],
        false,
    )
    .await;
    write_client_frame(&mut client, &ClientFrame::BodyEnd)
        .await
        .unwrap();
    write_client_frame(&mut client, &ClientFrame::Cancel)
        .await
        .unwrap();
    task.await.unwrap().unwrap();
}

#[tokio::test]
async fn client_disconnect_aborts_pending_connector_without_output() {
    let broker = broker(
        ScriptedResolver::answers(vec![public_ip()]),
        ScriptedConnector::steps([Step::Delayed(
            Duration::from_secs(1),
            Box::new(Step::Response(response(200, &[], []))),
        )]),
        healthy_audit(),
    );
    let (mut client, task) = start(broker);
    begin_request(
        &mut client,
        valid_token(),
        "GET",
        "https://public.example/",
        &[],
        false,
    )
    .await;
    write_client_frame(&mut client, &ClientFrame::BodyEnd)
        .await
        .unwrap();
    drop(client);
    tokio::time::timeout(Duration::from_millis(100), task)
        .await
        .expect("broker did not abort the upstream request after client disconnect")
        .unwrap()
        .unwrap();
}

#[tokio::test]
async fn cancel_drops_the_pending_connector_future() {
    let connector = PendingConnector::new();
    let started = Arc::clone(&connector.started);
    let dropped = Arc::clone(&connector.dropped);
    let broker = broker(
        ScriptedResolver::answers(vec![public_ip()]),
        connector,
        healthy_audit(),
    );
    let (mut client, task) = start(broker);
    begin_request(
        &mut client,
        valid_token(),
        "GET",
        "https://public.example/",
        &[],
        false,
    )
    .await;
    started.notified().await;
    write_client_frame(&mut client, &ClientFrame::Cancel)
        .await
        .unwrap();
    task.await.unwrap().unwrap();
    assert_eq!(dropped.load(Ordering::Acquire), 1);
}

#[tokio::test]
async fn disconnect_drops_the_pending_connector_future() {
    let connector = PendingConnector::new();
    let started = Arc::clone(&connector.started);
    let dropped = Arc::clone(&connector.dropped);
    let broker = broker(
        ScriptedResolver::answers(vec![public_ip()]),
        connector,
        healthy_audit(),
    );
    let (mut client, task) = start(broker);
    begin_request(
        &mut client,
        valid_token(),
        "GET",
        "https://public.example/",
        &[],
        false,
    )
    .await;
    started.notified().await;
    drop(client);
    task.await.unwrap().unwrap();
    assert_eq!(dropped.load(Ordering::Acquire), 1);
}

#[tokio::test]
async fn cancel_drops_the_pending_upstream_response_body() {
    let connector = PendingResponseConnector::new();
    let dropped = Arc::clone(&connector.dropped);
    let broker = broker(
        ScriptedResolver::answers(vec![public_ip()]),
        connector,
        healthy_audit(),
    );
    let (mut client, task) = start(broker);
    begin_request(
        &mut client,
        valid_token(),
        "GET",
        "https://public.example/",
        &[],
        false,
    )
    .await;
    write_client_frame(&mut client, &ClientFrame::BodyEnd)
        .await
        .unwrap();
    assert!(matches!(
        read_broker_frame(&mut client).await.unwrap(),
        BrokerFrame::ResponseHead(_)
    ));
    write_client_frame(&mut client, &ClientFrame::Cancel)
        .await
        .unwrap();
    task.await.unwrap().unwrap();
    assert_eq!(dropped.load(Ordering::Acquire), 1);
}

#[tokio::test]
async fn disconnect_drops_the_pending_upstream_response_body() {
    let connector = PendingResponseConnector::new();
    let dropped = Arc::clone(&connector.dropped);
    let broker = broker(
        ScriptedResolver::answers(vec![public_ip()]),
        connector,
        healthy_audit(),
    );
    let (mut client, task) = start(broker);
    begin_request(
        &mut client,
        valid_token(),
        "GET",
        "https://public.example/",
        &[],
        false,
    )
    .await;
    write_client_frame(&mut client, &ClientFrame::BodyEnd)
        .await
        .unwrap();
    assert!(matches!(
        read_broker_frame(&mut client).await.unwrap(),
        BrokerFrame::ResponseHead(_)
    ));
    drop(client);
    task.await.unwrap().unwrap();
    assert_eq!(dropped.load(Ordering::Acquire), 1);
}

#[cfg(target_os = "linux")]
#[tokio::test]
async fn linux_listener_uses_peer_credentials_mode_and_owned_socket_cleanup() {
    use std::os::unix::fs::PermissionsExt as _;
    use tempfile::tempdir;

    let root = tempdir().unwrap();
    let socket = root.path().join("broker.sock");
    let mut config = config();
    config.socket_path = socket.clone();
    config.peer_uid = unsafe { libc::geteuid() };
    config.peer_gid = unsafe { libc::getegid() };
    let broker = broker_with_config(
        config,
        ScriptedResolver::answers(vec![public_ip()]),
        ScriptedConnector::successes([]),
        healthy_audit(),
    );
    let server = tokio::spawn(async move { broker.serve().await });
    for _ in 0..50 {
        if socket.exists() {
            break;
        }
        sleep(Duration::from_millis(10)).await;
    }
    assert!(socket.exists());
    assert_eq!(
        std::fs::metadata(&socket).unwrap().permissions().mode() & 0o777,
        0o660
    );
    server.abort();
    let _ = server.await;
    for _ in 0..50 {
        if !socket.exists() {
            return;
        }
        sleep(Duration::from_millis(10)).await;
    }
    panic!("owned socket was not removed after broker shutdown");
}

fn config_env() -> HashMap<&'static str, &'static str> {
    HashMap::from([
        ("AGENT_FETCH_ENABLED", "true"),
        ("AGENT_FETCH_SOCKET", "/run/agent-fetch/broker.sock"),
        ("AGENT_FETCH_PEER_UID", "10001"),
        ("AGENT_FETCH_PEER_GID", "10002"),
        ("AGENT_FETCH_HMAC_KEY_FILE", "/run/secrets/key"),
        ("AGENT_FETCH_DENY_CIDRS", "127.0.0.0/8,::1/128"),
        (
            "AGENT_FETCH_DNS_SERVERS",
            "1.1.1.1:53,[2606:4700:4700::1111]:53",
        ),
        ("AGENT_FETCH_AUDIT_PATH", "/var/log/fetch.jsonl"),
        ("AGENT_FETCH_POLICY_VERSION", "policy-v1"),
    ])
}

fn config() -> BrokerConfig {
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

fn broker<R, C>(
    resolver: R,
    connector: C,
    audit: JsonlAuditSink,
) -> FetchBroker<R, C, JsonlAuditSink>
where
    R: Resolver + 'static,
    C: PinnedConnector + 'static,
{
    broker_with_config(config(), resolver, connector, audit)
}

fn broker_with_config<R, C>(
    config: BrokerConfig,
    resolver: R,
    connector: C,
    audit: JsonlAuditSink,
) -> FetchBroker<R, C, JsonlAuditSink>
where
    R: Resolver + 'static,
    C: PinnedConnector + 'static,
{
    FetchBroker::with_components(config, KEY, resolver, connector, audit).unwrap()
}

fn healthy_audit() -> JsonlAuditSink {
    JsonlAuditSink::with_writer(ControlledWriter::never())
}

fn valid_token() -> SecretString {
    valid_token_with_limits(8 * 1024 * 1024, 32 * 1024 * 1024)
}

fn valid_token_for(config: &BrokerConfig) -> SecretString {
    valid_token_with_limits(
        config.request_body_max_bytes,
        config.response_decoded_max_bytes,
    )
}

fn valid_token_with_limits(max_request_bytes: u64, max_response_bytes: u64) -> SecretString {
    valid_token_with_claims(2, 20, max_request_bytes, max_response_bytes)
}

fn valid_token_with_claims(
    max_concurrency: u16,
    max_requests: u16,
    max_request_bytes: u64,
    max_response_bytes: u64,
) -> SecretString {
    let now = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_secs() as i64;
    TokenIssuer::new(KEY)
        .unwrap()
        .issue(&FetchClaims {
            protocol_version: FETCH_PROTOCOL_VERSION,
            policy_version: "policy-v1".to_string(),
            namespace: "namespace-secret".to_string(),
            run_id: "run-secret".to_string(),
            command_id: "command-secret".to_string(),
            issued_at_unix: now,
            expires_at_unix: now + 60,
            max_concurrency,
            max_requests,
            max_request_bytes,
            max_response_bytes,
        })
        .unwrap()
}

fn runtime_token_for(config: &BrokerConfig) -> SecretString {
    RuntimeFetchSecurity::new(
        "unused.sock",
        KEY,
        FetchClaimLimits {
            protocol_version: FETCH_PROTOCOL_VERSION,
            policy_version: config.policy_version.clone(),
            max_concurrency: config.max_concurrency,
            max_requests: config.max_requests,
            max_request_bytes: config.request_body_max_bytes,
            max_response_bytes: config.response_decoded_max_bytes,
        },
    )
    .unwrap()
    .issue_for_command(
        "namespace-secret",
        "run-secret",
        "command-secret".to_string(),
        Duration::from_secs(30),
        SystemTime::now(),
    )
    .unwrap()
    .token
}

fn hello() -> ClientHello {
    ClientHello {
        protocol_version: FETCH_PROTOCOL_VERSION,
    }
}

fn public_ip() -> IpAddr {
    IpAddr::V4(Ipv4Addr::new(8, 8, 8, 8))
}

fn start<R, C>(
    broker: FetchBroker<R, C, JsonlAuditSink>,
) -> (
    DuplexStream,
    tokio::task::JoinHandle<Result<(), agent_runtime::fetch_broker::BrokerError>>,
)
where
    R: Resolver + 'static,
    C: PinnedConnector + 'static,
{
    let (client, server) = tokio::io::duplex(128 * 1024);
    let task = tokio::spawn(async move { broker.serve_connection(server, PEER).await });
    (client, task)
}

fn start_with_peer<R, C>(
    state: Arc<BrokerState<R, C, JsonlAuditSink>>,
    peer: PeerCred,
) -> (
    DuplexStream,
    tokio::task::JoinHandle<Result<(), agent_runtime::fetch_broker::BrokerError>>,
)
where
    R: Resolver + 'static,
    C: PinnedConnector + 'static,
{
    start_state(state, peer)
}

fn start_state<R, C>(
    state: Arc<BrokerState<R, C, JsonlAuditSink>>,
    peer: PeerCred,
) -> (
    DuplexStream,
    tokio::task::JoinHandle<Result<(), agent_runtime::fetch_broker::BrokerError>>,
)
where
    R: Resolver + 'static,
    C: PinnedConnector + 'static,
{
    let (client, server) = tokio::io::duplex(128 * 1024);
    let task = tokio::spawn(async move { serve_connection(server, peer, state).await });
    (client, task)
}

async fn begin_request(
    client: &mut DuplexStream,
    token: SecretString,
    method: &str,
    url: &str,
    headers: &[(&str, &str)],
    follow: bool,
) {
    begin_request_head(client, token, request_head(method, url, headers, follow)).await;
}

async fn begin_request_head(
    client: &mut DuplexStream,
    token: SecretString,
    request: FetchRequestHead,
) {
    authenticate_client(client, token).await;
    write_client_frame(client, &ClientFrame::Request(request))
        .await
        .unwrap();
    assert!(matches!(
        read_broker_frame(client).await.unwrap(),
        BrokerFrame::Continue
    ));
}

async fn authenticate_client(client: &mut DuplexStream, token: SecretString) {
    write_client_frame(client, &ClientFrame::Hello(hello()))
        .await
        .unwrap();
    assert!(matches!(
        read_broker_frame(client).await.unwrap(),
        BrokerFrame::Hello(_)
    ));
    write_client_frame(
        client,
        &ClientFrame::Auth(AuthMetadata {
            protocol_version: FETCH_PROTOCOL_VERSION,
            token,
        }),
    )
    .await
    .unwrap();
    assert!(matches!(
        read_broker_frame(client).await.unwrap(),
        BrokerFrame::Authenticated
    ));
}

async fn encoded_client_frame(frame: ClientFrame) -> Vec<u8> {
    let mut bytes = Vec::new();
    write_client_frame(&mut bytes, &frame).await.unwrap();
    bytes
}

fn request_head(
    method: &str,
    url: &str,
    headers: &[(&str, &str)],
    follow: bool,
) -> FetchRequestHead {
    FetchRequestHead {
        protocol_version: FETCH_PROTOCOL_VERSION,
        method: method.to_string(),
        url: url.to_string(),
        headers: headers
            .iter()
            .map(|(name, value)| ((*name).to_string(), (*value).to_string()))
            .collect(),
        follow,
        check_status: false,
        timeout_ms: None,
        declared_body_bytes: Some(0),
    }
}

async fn read_success(
    client: &mut DuplexStream,
) -> (agent_runtime::fetch_protocol::FetchResponseHead, Vec<u8>) {
    let head = match read_broker_frame(client).await.unwrap() {
        BrokerFrame::ResponseHead(head) => head,
        other => panic!("expected response head, got {other:?}"),
    };
    let mut body = Vec::new();
    loop {
        match read_broker_frame(client).await.unwrap() {
            BrokerFrame::ResponseChunk(chunk) => body.extend_from_slice(&chunk),
            BrokerFrame::ResponseEnd(end) => {
                assert_eq!(end.body_bytes as usize, body.len());
                return (head, body);
            }
            other => panic!("expected response data, got {other:?}"),
        }
    }
}

async fn assert_error(client: &mut DuplexStream, code: ErrorCode) {
    match read_broker_frame(client).await.unwrap() {
        BrokerFrame::Error(error) => assert_eq!(error.code, code),
        other => panic!("expected error frame, got {other:?}"),
    }
}

async fn timeout_case<R>(config: BrokerConfig, resolver: R, connector: ScriptedConnector)
where
    R: Resolver + 'static,
{
    let broker = broker_with_config(config, resolver, connector, healthy_audit());
    let (mut client, task) = start(broker);
    begin_request(
        &mut client,
        valid_token(),
        "GET",
        "https://public.example/",
        &[],
        false,
    )
    .await;
    write_client_frame(&mut client, &ClientFrame::BodyEnd)
        .await
        .unwrap();
    assert_error(&mut client, ErrorCode::Timeout).await;
    task.await.unwrap().unwrap();
}

async fn response_limit_case(config: BrokerConfig, response: UpstreamResponse) {
    response_limit_case_with_token(config.clone(), valid_token_for(&config), response).await;
}

async fn response_limit_case_with_token(
    config: BrokerConfig,
    token: SecretString,
    response: UpstreamResponse,
) {
    let broker = broker_with_config(
        config,
        ScriptedResolver::answers(vec![public_ip()]),
        ScriptedConnector::successes([response]),
        healthy_audit(),
    );
    let (mut client, task) = start(broker);
    begin_request(
        &mut client,
        token,
        "GET",
        "https://public.example/",
        &[],
        false,
    )
    .await;
    write_client_frame(&mut client, &ClientFrame::BodyEnd)
        .await
        .unwrap();
    assert!(matches!(
        read_broker_frame(&mut client).await.unwrap(),
        BrokerFrame::ResponseHead(_)
    ));
    assert_error(&mut client, ErrorCode::Policy).await;
    task.await.unwrap().unwrap();
}

async fn decoded_response(encoding: &str, compressed: Vec<u8>) -> Vec<u8> {
    let broker = broker(
        ScriptedResolver::answers(vec![public_ip()]),
        ScriptedConnector::successes([response(
            200,
            &[("content-encoding", encoding)],
            [compressed.as_slice()],
        )]),
        healthy_audit(),
    );
    let (mut client, task) = start(broker);
    begin_request(
        &mut client,
        valid_token(),
        "GET",
        "https://public.example/",
        &[],
        false,
    )
    .await;
    write_client_frame(&mut client, &ClientFrame::BodyEnd)
        .await
        .unwrap();
    let (_, body) = read_success(&mut client).await;
    task.await.unwrap().unwrap();
    body
}

fn response<'a>(
    status: u16,
    headers: &[(&str, &str)],
    chunks: impl IntoIterator<Item = &'a [u8]>,
) -> UpstreamResponse {
    let mut map = HeaderMap::new();
    for (name, value) in headers {
        map.append(
            HeaderName::from_bytes(name.as_bytes()).unwrap(),
            HeaderValue::try_from(*value).unwrap(),
        );
    }
    UpstreamResponse::from_chunks(
        StatusCode::from_u16(status).unwrap(),
        map,
        chunks
            .into_iter()
            .map(|chunk| Ok(Bytes::copy_from_slice(chunk)))
            .collect::<Vec<_>>(),
    )
}

fn partial_then_pending_response(chunk: &'static [u8]) -> UpstreamResponse {
    UpstreamResponse {
        status: StatusCode::OK,
        reason: "OK".to_string(),
        headers: HeaderMap::new(),
        body: Box::pin(
            futures_util::stream::once(async move { Ok(Bytes::from_static(chunk)) })
                .chain(futures_util::stream::pending()),
        ),
    }
}

fn sha256_hex(bytes: &[u8]) -> String {
    Sha256::digest(bytes)
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

struct ScriptedResolver {
    steps: Mutex<VecDeque<ResolveStep>>,
    calls: Arc<AtomicUsize>,
}

enum ResolveStep {
    Answers(Vec<IpAddr>),
    Delayed(Duration, Vec<IpAddr>),
    Failure(ResolveError),
}

impl ScriptedResolver {
    fn answers(answers: Vec<IpAddr>) -> Self {
        Self {
            steps: Mutex::new(VecDeque::from([ResolveStep::Answers(answers)])),
            calls: Arc::new(AtomicUsize::new(0)),
        }
    }

    fn delayed(delay: Duration, answers: Vec<IpAddr>) -> Self {
        Self {
            steps: Mutex::new(VecDeque::from([ResolveStep::Delayed(delay, answers)])),
            calls: Arc::new(AtomicUsize::new(0)),
        }
    }

    fn failure(error: ResolveError) -> Self {
        Self {
            steps: Mutex::new(VecDeque::from([ResolveStep::Failure(error)])),
            calls: Arc::new(AtomicUsize::new(0)),
        }
    }
}

#[async_trait]
impl Resolver for ScriptedResolver {
    async fn resolve_all(&self, _host: &str) -> Result<Vec<IpAddr>, ResolveError> {
        self.calls.fetch_add(1, Ordering::Relaxed);
        let step = self
            .steps
            .lock()
            .unwrap()
            .pop_front()
            .unwrap_or(ResolveStep::Answers(vec![public_ip()]));
        match step {
            ResolveStep::Answers(answers) => Ok(answers),
            ResolveStep::Delayed(delay, answers) => {
                sleep(delay).await;
                Ok(answers)
            }
            ResolveStep::Failure(error) => Err(error),
        }
    }
}

struct ScriptedConnector {
    steps: Mutex<VecDeque<Step>>,
    calls: Arc<Mutex<Vec<ConnectorCall>>>,
}

enum Step {
    Response(UpstreamResponse),
    Failure(ConnectError),
    Delayed(Duration, Box<Step>),
}

#[derive(Debug)]
struct ConnectorCall {
    approved_ip: IpAddr,
    has_authorization: bool,
}

impl ScriptedConnector {
    fn successes(responses: impl IntoIterator<Item = UpstreamResponse>) -> Self {
        Self::steps(responses.into_iter().map(Step::Response))
    }

    fn steps(steps: impl IntoIterator<Item = Step>) -> Self {
        Self {
            steps: Mutex::new(steps.into_iter().collect()),
            calls: Arc::new(Mutex::new(Vec::new())),
        }
    }
}

#[async_trait]
impl PinnedConnector for ScriptedConnector {
    async fn execute(
        &self,
        request: ReviewedRequest,
        target: agent_runtime::fetch_policy::ApprovedTarget,
        _body: BodyStream,
    ) -> Result<UpstreamResponse, ConnectError> {
        self.calls.lock().unwrap().push(ConnectorCall {
            approved_ip: target.addresses[0].ip(),
            has_authorization: request.headers.contains("authorization"),
        });
        let step = self
            .steps
            .lock()
            .unwrap()
            .pop_front()
            .unwrap_or(Step::Failure(ConnectError::Failed));
        run_step(step).await
    }
}

struct DrainingConnector {
    chunks: Arc<Mutex<Vec<Vec<u8>>>>,
}

impl DrainingConnector {
    fn new() -> Self {
        Self {
            chunks: Arc::new(Mutex::new(Vec::new())),
        }
    }
}

#[async_trait]
impl PinnedConnector for DrainingConnector {
    async fn execute(
        &self,
        _request: ReviewedRequest,
        _target: agent_runtime::fetch_policy::ApprovedTarget,
        mut body: BodyStream,
    ) -> Result<UpstreamResponse, ConnectError> {
        while let Some(chunk) = body.next().await {
            self.chunks.lock().unwrap().push(chunk?.to_vec());
        }
        Ok(response(200, &[], []))
    }
}

struct GatedStreamingConnector {
    started: Arc<Notify>,
    allow_first: Arc<Notify>,
    first_chunk: Arc<Notify>,
    allow_rest: Arc<Notify>,
    observed: Arc<AtomicUsize>,
}

impl GatedStreamingConnector {
    fn new() -> Self {
        Self {
            started: Arc::new(Notify::new()),
            allow_first: Arc::new(Notify::new()),
            first_chunk: Arc::new(Notify::new()),
            allow_rest: Arc::new(Notify::new()),
            observed: Arc::new(AtomicUsize::new(0)),
        }
    }
}

#[async_trait]
impl PinnedConnector for GatedStreamingConnector {
    async fn execute(
        &self,
        _request: ReviewedRequest,
        _target: agent_runtime::fetch_policy::ApprovedTarget,
        mut body: BodyStream,
    ) -> Result<UpstreamResponse, ConnectError> {
        self.started.notify_one();
        self.allow_first.notified().await;
        if let Some(chunk) = body.next().await {
            let _ = chunk?;
            self.observed.fetch_add(1, Ordering::Relaxed);
            self.first_chunk.notify_one();
        }
        self.allow_rest.notified().await;
        while let Some(chunk) = body.next().await {
            let _ = chunk?;
            self.observed.fetch_add(1, Ordering::Relaxed);
        }
        Ok(response(200, &[], []))
    }
}

struct PendingConnector {
    started: Arc<Notify>,
    dropped: Arc<AtomicUsize>,
}

impl PendingConnector {
    fn new() -> Self {
        Self {
            started: Arc::new(Notify::new()),
            dropped: Arc::new(AtomicUsize::new(0)),
        }
    }
}

#[async_trait]
impl PinnedConnector for PendingConnector {
    async fn execute(
        &self,
        _request: ReviewedRequest,
        _target: agent_runtime::fetch_policy::ApprovedTarget,
        _body: BodyStream,
    ) -> Result<UpstreamResponse, ConnectError> {
        let _drop = ConnectorDrop(Arc::clone(&self.dropped));
        self.started.notify_one();
        std::future::pending().await
    }
}

struct ConnectorDrop(Arc<AtomicUsize>);

impl Drop for ConnectorDrop {
    fn drop(&mut self) {
        self.0.fetch_add(1, Ordering::Release);
    }
}

struct PendingResponseConnector {
    dropped: Arc<AtomicUsize>,
}

impl PendingResponseConnector {
    fn new() -> Self {
        Self {
            dropped: Arc::new(AtomicUsize::new(0)),
        }
    }
}

#[async_trait]
impl PinnedConnector for PendingResponseConnector {
    async fn execute(
        &self,
        _request: ReviewedRequest,
        _target: agent_runtime::fetch_policy::ApprovedTarget,
        _body: BodyStream,
    ) -> Result<UpstreamResponse, ConnectError> {
        Ok(UpstreamResponse {
            status: StatusCode::OK,
            reason: "OK".to_string(),
            headers: HeaderMap::new(),
            body: Box::pin(PendingResponseBody(Arc::clone(&self.dropped))),
        })
    }
}

struct PendingResponseBody(Arc<AtomicUsize>);

impl futures_util::Stream for PendingResponseBody {
    type Item = Result<Bytes, ConnectError>;

    fn poll_next(
        self: std::pin::Pin<&mut Self>,
        _context: &mut Context<'_>,
    ) -> Poll<Option<Self::Item>> {
        Poll::Pending
    }
}

impl Drop for PendingResponseBody {
    fn drop(&mut self) {
        self.0.fetch_add(1, Ordering::Release);
    }
}

async fn run_step(mut step: Step) -> Result<UpstreamResponse, ConnectError> {
    loop {
        match step {
            Step::Response(response) => return Ok(response),
            Step::Failure(error) => return Err(error),
            Step::Delayed(delay, next) => {
                sleep(delay).await;
                step = *next;
            }
        }
    }
}

struct ControlledWriter {
    calls: usize,
    fail_on: Option<usize>,
}

impl ControlledWriter {
    fn never() -> Self {
        Self {
            calls: 0,
            fail_on: None,
        }
    }

    fn failing_on(call: usize) -> Self {
        Self {
            calls: 0,
            fail_on: Some(call),
        }
    }
}

impl AuditWriter for ControlledWriter {
    fn append(&mut self, _serialized_record: &[u8]) -> std::io::Result<()> {
        self.calls += 1;
        if self.fail_on == Some(self.calls) {
            return Err(std::io::Error::other("injected audit failure"));
        }
        Ok(())
    }
}

struct RecordingWriter {
    records: Arc<Mutex<Vec<Vec<u8>>>>,
}

impl RecordingWriter {
    fn new(records: Arc<Mutex<Vec<Vec<u8>>>>) -> Self {
        Self { records }
    }
}

impl AuditWriter for RecordingWriter {
    fn append(&mut self, serialized_record: &[u8]) -> std::io::Result<()> {
        self.records
            .lock()
            .unwrap()
            .push(serialized_record.to_vec());
        Ok(())
    }
}
