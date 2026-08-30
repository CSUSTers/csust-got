#[cfg(all(target_os = "linux", feature = "c7-test-support"))]
use agent_runtime::fetch_protocol::ErrorCode;
use agent_runtime::{
    fetch_protocol::{FETCH_PROTOCOL_VERSION, FetchRequestHead},
    runtime_fetch_proxy::{
        CommandBindingPhase, CommandControlPacket, LOCAL_SESSION_CHANNEL_CAPACITY,
        LocalRequestState, LocalResponseState, MAX_COMMAND_CONTROL_PACKET_BYTES, OutputCommitGuard,
    },
    workspace_budget::WorkspaceBudget,
};
use sha2::{Digest as _, Sha256};
use std::sync::{Arc, Mutex};

fn storage_key(namespace: &str) -> String {
    Sha256::digest(namespace.as_bytes())
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

#[cfg(target_os = "linux")]
mod linux_proxy {
    use super::*;
    use agent_runtime::{
        audit::JsonlAuditSink,
        config::FetchClaimLimits,
        exec::BashHealth,
        fetch_auth::{BrokerAuthCaps, TokenVerifier},
        fetch_broker::{
            BodyStream, BrokerConfig, ConnectError, FetchBroker, PeerCred, PinnedConnector,
            ResolveError, Resolver, ReviewedRequest, UpstreamResponse,
        },
        fetch_policy::ApprovedTarget,
        fetch_protocol::{
            BrokerFrame, BrokerHello, ClientFrame, ErrorCode, FetchProtocolErrorFrame,
            FetchResponseEnd, FetchResponseHead, LocalClientFrame, LocalRuntimeFrame,
            read_client_frame, read_local_runtime_frame, write_broker_frame,
            write_local_client_frame,
        },
        runtime_fetch_proxy::{
            ControlReaderOutcome, MAX_ACTIVE_LOCAL_SESSIONS, MAX_COMMAND_CONTROL_PACKETS,
            SESSION_JOB_CHANNEL_CAPACITY,
        },
        runtime_security::RuntimeFetchSecurity,
    };
    use async_trait::async_trait;
    use http::{HeaderMap, StatusCode};
    use std::{
        io,
        mem::{MaybeUninit, size_of},
        net::{IpAddr, Ipv4Addr, SocketAddr},
        os::fd::{AsRawFd, FromRawFd, OwnedFd},
        path::PathBuf,
        process::Stdio,
        sync::{
            Mutex,
            atomic::{AtomicUsize, Ordering},
        },
        time::{Duration, SystemTime},
    };

    const KEY: &[u8] = b"runtime proxy integration key with enough entropy";

    struct IdentityResolver;

    #[async_trait]
    impl Resolver for IdentityResolver {
        async fn resolve_all(&self, _host: &str) -> Result<Vec<IpAddr>, ResolveError> {
            Ok(vec![IpAddr::V4(Ipv4Addr::new(8, 8, 8, 8))])
        }
    }

    struct IdentityConnector;

    #[async_trait]
    impl PinnedConnector for IdentityConnector {
        async fn execute(
            &self,
            _request: ReviewedRequest,
            _target: ApprovedTarget,
            _body: BodyStream,
        ) -> Result<UpstreamResponse, ConnectError> {
            Ok(UpstreamResponse::from_chunks(
                StatusCode::OK,
                HeaderMap::new(),
                [Ok(bytes::Bytes::from_static(b"identity output"))],
            ))
        }
    }

    fn identity_broker_config(socket_path: PathBuf, audit_path: PathBuf) -> BrokerConfig {
        BrokerConfig {
            socket_path,
            peer_uid: 1,
            peer_gid: 2,
            hmac_key_file: PathBuf::from("unused-key"),
            deny_cidrs: Vec::new(),
            dns_servers: vec![SocketAddr::from(([8, 8, 8, 8], 53))],
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
            pre_auth_connections: 1,
            handshake_timeout: Duration::from_secs(2),
            max_concurrency: 2,
            max_requests: 20,
            max_redirects: 5,
            audit_path,
            policy_version: "policy-v1".to_string(),
        }
    }

    fn sha256_hex(value: &str) -> String {
        storage_key(value)
    }

    #[tokio::test]
    async fn copied_fd_number_is_bound_to_the_receiving_command_not_the_source_strings() {
        let fixture = tempfile::tempdir().unwrap();
        let workspace = fixture.path().join("workspaces");
        std::fs::create_dir(&workspace).unwrap();
        let socket = fixture.path().join("broker.sock");
        let listener = tokio::net::UnixListener::bind(&socket).unwrap();
        let verifier = token_verifier();
        let broker = tokio::spawn(async move {
            let (mut stream, _) = listener.accept().await.unwrap();
            assert!(matches!(
                read_client_frame(&mut stream).await.unwrap(),
                ClientFrame::Hello(_)
            ));
            write_broker_frame(
                &mut stream,
                &BrokerFrame::Hello(BrokerHello {
                    protocol_version: FETCH_PROTOCOL_VERSION,
                }),
            )
            .await
            .unwrap();
            let token = match read_client_frame(&mut stream).await.unwrap() {
                ClientFrame::Auth(auth) => auth.token,
                frame => panic!("expected auth, got {frame:?}"),
            };
            let verified = verifier.verify(&token, SystemTime::now()).unwrap();
            assert_eq!(verified.claims.namespace, "receiver-b");
            assert_eq!(verified.claims.run_id, "run-b");
            assert_eq!(verified.claims.command_id, "command-b");
            write_broker_frame(&mut stream, &BrokerFrame::Authenticated)
                .await
                .unwrap();
            assert!(matches!(
                read_client_frame(&mut stream).await.unwrap(),
                ClientFrame::Request(_)
            ));
            write_broker_frame(&mut stream, &BrokerFrame::Continue)
                .await
                .unwrap();
            assert!(matches!(
                read_client_frame(&mut stream).await.unwrap(),
                ClientFrame::BodyEnd
            ));
            write_broker_frame(
                &mut stream,
                &BrokerFrame::ResponseHead(FetchResponseHead {
                    protocol_version: FETCH_PROTOCOL_VERSION,
                    status: 200,
                    reason: "OK".to_string(),
                    headers: Vec::new(),
                }),
            )
            .await
            .unwrap();
            write_broker_frame(
                &mut stream,
                &BrokerFrame::ResponseEnd(FetchResponseEnd {
                    protocol_version: FETCH_PROTOCOL_VERSION,
                    body_bytes: 0,
                }),
            )
            .await
            .unwrap();
        });
        let proxy = enabled_proxy(&socket, &workspace);
        let binding = proxy
            .bind_command(
                "receiver-b",
                "run-b",
                "command-b".to_string(),
                Duration::from_secs(5),
                BashHealth::ready(),
            )
            .unwrap();
        let launch = binding.into_launch(proxy.shell_environment()).unwrap();
        let (mut local, transferred) = local_session_pair();
        send_control_packet(
            launch.control_source.as_raw_fd(),
            &serde_json::to_vec(&packet()).unwrap(),
            &[transferred.as_raw_fd()],
        )
        .unwrap();
        drop(transferred);

        assert_eq!(
            read_local_runtime_frame(&mut local).await.unwrap(),
            LocalRuntimeFrame::Continue
        );
        write_local_client_frame(&mut local, &LocalClientFrame::BodyEnd)
            .await
            .unwrap();
        assert!(matches!(
            read_local_runtime_frame(&mut local).await.unwrap(),
            LocalRuntimeFrame::ResponseHead(_)
        ));
        assert!(matches!(
            read_local_runtime_frame(&mut local).await.unwrap(),
            LocalRuntimeFrame::ResponseEnd(_)
        ));
        broker.await.unwrap();
        launch.lifecycle.revoke_and_wait().await.unwrap();
        assert_eq!(proxy.active_binding_count().unwrap(), 0);
    }

    #[tokio::test]
    async fn command_bound_fetch_output_hashes_namespace_once_and_preserves_identity() {
        let fixture = tempfile::tempdir().unwrap();
        let workspace = fixture.path().join("workspaces");
        std::fs::create_dir(&workspace).unwrap();
        let socket = fixture.path().join("broker.sock");
        let audit_path = fixture.path().join("audit.jsonl");
        let broker = FetchBroker::with_components(
            identity_broker_config(socket.clone(), audit_path.clone()),
            KEY,
            IdentityResolver,
            IdentityConnector,
            JsonlAuditSink::open(&audit_path).await.unwrap(),
        )
        .unwrap();
        let listener = tokio::net::UnixListener::bind(&socket).unwrap();
        let broker_task = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.unwrap();
            broker
                .serve_connection(stream, PeerCred { uid: 1, gid: 2 })
                .await
                .unwrap();
        });
        let namespace = "tenant/a:b";
        let namespace_key = sha256_hex(namespace);
        let proxy = enabled_proxy(&socket, &workspace);
        let launch = proxy
            .bind_command(
                namespace,
                "original-run",
                "identity-output".to_string(),
                Duration::from_secs(5),
                BashHealth::ready(),
            )
            .unwrap()
            .into_launch(proxy.shell_environment())
            .unwrap();
        let (mut local, transferred) = local_session_pair();
        let mut control = packet();
        control.output_path = Some("/workspace/result.txt".to_string());
        send_control_packet(
            launch.control_source.as_raw_fd(),
            &serde_json::to_vec(&control).unwrap(),
            &[transferred.as_raw_fd()],
        )
        .unwrap();
        drop(transferred);

        assert_eq!(
            read_local_runtime_frame(&mut local).await.unwrap(),
            LocalRuntimeFrame::Continue
        );
        write_local_client_frame(&mut local, &LocalClientFrame::BodyEnd)
            .await
            .unwrap();
        assert!(matches!(
            read_local_runtime_frame(&mut local).await.unwrap(),
            LocalRuntimeFrame::ResponseHead(FetchResponseHead { status: 200, .. })
        ));
        assert!(matches!(
            read_local_runtime_frame(&mut local).await.unwrap(),
            LocalRuntimeFrame::ResponseEnd(end) if end.output_committed
        ));
        broker_task.await.unwrap();
        launch.lifecycle.revoke_and_wait().await.unwrap();

        let audit = std::fs::read_to_string(&audit_path).unwrap();
        let start = audit
            .lines()
            .map(|line| serde_json::from_str::<serde_json::Value>(line).unwrap())
            .find(|record| record["event"] == "start")
            .expect("broker must write its token-derived audit start record");
        assert_eq!(start["namespace_sha256"], sha256_hex(namespace));
        assert_ne!(start["namespace_sha256"], sha256_hex(&namespace_key));

        let destination = workspace.join(&namespace_key).join("result.txt");
        assert_eq!(std::fs::read(&destination).unwrap(), b"identity output");
        assert!(
            !workspace
                .join(sha256_hex(&namespace_key))
                .join("result.txt")
                .exists()
        );
        let workspace_directories = std::fs::read_dir(&workspace)
            .unwrap()
            .filter_map(Result::ok)
            .filter(|entry| entry.file_type().is_ok_and(|kind| kind.is_dir()))
            .count();
        assert_eq!(workspace_directories, 1);
        assert_no_output_temporary(destination.parent().unwrap());
    }

    #[tokio::test]
    async fn endpoint_retained_by_another_process_is_unusable_after_owner_exit() {
        let fixture = tempfile::tempdir().unwrap();
        let workspace = fixture.path().join("workspaces");
        std::fs::create_dir(&workspace).unwrap();
        let socket = fixture.path().join("broker.sock");
        let listener = tokio::net::UnixListener::bind(&socket).unwrap();
        let connects = Arc::new(AtomicUsize::new(0));
        let observed = Arc::clone(&connects);
        let broker = tokio::spawn(async move {
            if let Ok(Ok((_stream, _))) =
                tokio::time::timeout(Duration::from_millis(250), listener.accept()).await
            {
                observed.fetch_add(1, Ordering::SeqCst);
            }
        });
        let proxy = enabled_proxy(&socket, &workspace);
        let launch = proxy
            .bind_command(
                "namespace",
                "run",
                "owner-exit".to_string(),
                Duration::from_secs(5),
                BashHealth::ready(),
            )
            .unwrap()
            .into_launch(proxy.shell_environment())
            .unwrap();
        let copied_raw =
            unsafe { libc::fcntl(launch.control_source.as_raw_fd(), libc::F_DUPFD_CLOEXEC, 8) };
        assert!(copied_raw >= 8);
        let copied = unsafe { OwnedFd::from_raw_fd(copied_raw) };

        launch.lifecycle.revoke_and_wait().await.unwrap();
        assert_eq!(
            launch.lifecycle.phase().unwrap(),
            Some(CommandBindingPhase::Drained)
        );
        assert_eq!(proxy.active_binding_count().unwrap(), 0);
        let (mut local, transferred) = local_session_pair();
        if send_control_packet(
            copied.as_raw_fd(),
            &serde_json::to_vec(&packet()).unwrap(),
            &[transferred.as_raw_fd()],
        )
        .is_ok()
        {
            drop(transferred);
            assert!(
                tokio::time::timeout(
                    Duration::from_millis(100),
                    read_local_runtime_frame(&mut local),
                )
                .await
                .unwrap()
                .is_err()
            );
        }
        broker.await.unwrap();
        assert_eq!(connects.load(Ordering::SeqCst), 0);

        let status = std::process::Command::new(env!("CARGO_BIN_EXE_fetch"))
            .arg("https://example.com/")
            .env_clear()
            .env("AGENT_FETCH_CONTROL_FD", "4")
            .status()
            .unwrap();
        assert_eq!(status.code(), Some(69));
    }

    #[tokio::test]
    async fn malformed_truncated_zero_and_extra_rights_count_toward_twenty_and_sessions_stay_at_two()
     {
        let fixture = tempfile::tempdir().unwrap();
        let workspace = fixture.path().join("workspaces");
        std::fs::create_dir(&workspace).unwrap();
        let socket = fixture.path().join("broker.sock");
        let listener = tokio::net::UnixListener::bind(&socket).unwrap();
        let connects = Arc::new(AtomicUsize::new(0));
        let observed = Arc::clone(&connects);
        let broker = tokio::spawn(async move {
            let mut streams = Vec::new();
            while let Ok(Ok((stream, _))) =
                tokio::time::timeout(Duration::from_millis(300), listener.accept()).await
            {
                observed.fetch_add(1, Ordering::SeqCst);
                streams.push(stream);
            }
        });
        let proxy = enabled_proxy(&socket, &workspace);
        let launch = proxy
            .bind_command(
                "namespace",
                "run",
                "packet-limit".to_string(),
                Duration::from_secs(5),
                BashHealth::ready(),
            )
            .unwrap()
            .into_launch(proxy.shell_environment())
            .unwrap();
        for _ in 0..(MAX_COMMAND_CONTROL_PACKETS - 3) {
            send_control_packet(launch.control_source.as_raw_fd(), b"{}", &[]).unwrap();
        }
        let (_first, first_right) = local_session_pair();
        let (_second, second_right) = local_session_pair();
        send_control_packet(
            launch.control_source.as_raw_fd(),
            b"{}",
            &[first_right.as_raw_fd(), second_right.as_raw_fd()],
        )
        .unwrap();
        let (_truncated_local, truncated_right) = local_session_pair();
        send_control_packet(
            launch.control_source.as_raw_fd(),
            &vec![b'x'; MAX_COMMAND_CONTROL_PACKET_BYTES + 1],
            &[truncated_right.as_raw_fd()],
        )
        .unwrap();
        let (_ancillary_local, ancillary_right) = local_session_pair();
        let excessive_rights = vec![ancillary_right.as_raw_fd(); 64];
        send_control_packet(
            launch.control_source.as_raw_fd(),
            &serde_json::to_vec(&packet()).unwrap(),
            &excessive_rights,
        )
        .unwrap();
        let (_local, transferred) = local_session_pair();
        tokio::time::timeout(Duration::from_secs(1), async {
            loop {
                if send_control_packet(
                    launch.control_source.as_raw_fd(),
                    &serde_json::to_vec(&packet()).unwrap(),
                    &[transferred.as_raw_fd()],
                )
                .is_err()
                {
                    break;
                }
                tokio::task::yield_now().await;
            }
        })
        .await
        .unwrap();
        launch.lifecycle.revoke_and_wait().await.unwrap();
        broker.await.unwrap();
        assert_eq!(connects.load(Ordering::SeqCst), 0);

        std::fs::remove_file(&socket).unwrap();
        let listener = tokio::net::UnixListener::bind(&socket).unwrap();
        let connects = Arc::new(AtomicUsize::new(0));
        let observed = Arc::clone(&connects);
        let (accepted_tx, mut accepted_rx) = tokio::sync::mpsc::channel(3);
        let broker = tokio::spawn(async move {
            let mut streams = Vec::new();
            while let Ok(Ok((stream, _))) =
                tokio::time::timeout(Duration::from_millis(300), listener.accept()).await
            {
                observed.fetch_add(1, Ordering::SeqCst);
                accepted_tx.send(()).await.unwrap();
                streams.push(stream);
            }
        });
        let launch = proxy
            .bind_command(
                "namespace",
                "run",
                "session-limit".to_string(),
                Duration::from_secs(5),
                BashHealth::ready(),
            )
            .unwrap()
            .into_launch(proxy.shell_environment())
            .unwrap();
        let mut peers = Vec::new();
        for _ in 0..MAX_ACTIVE_LOCAL_SESSIONS {
            let (local, transferred) = local_session_pair();
            send_control_packet(
                launch.control_source.as_raw_fd(),
                &serde_json::to_vec(&packet()).unwrap(),
                &[transferred.as_raw_fd()],
            )
            .unwrap();
            peers.push((local, transferred));
            accepted_rx.recv().await.unwrap();
        }
        let (local, transferred) = local_session_pair();
        send_control_packet(
            launch.control_source.as_raw_fd(),
            &serde_json::to_vec(&packet()).unwrap(),
            &[transferred.as_raw_fd()],
        )
        .unwrap();
        peers.push((local, transferred));
        assert!(
            tokio::time::timeout(Duration::from_millis(100), accepted_rx.recv())
                .await
                .is_err()
        );
        launch.lifecycle.revoke_and_wait().await.unwrap();
        broker.await.unwrap();
        assert_eq!(connects.load(Ordering::SeqCst), MAX_ACTIVE_LOCAL_SESSIONS);
    }

    #[tokio::test]
    async fn body_before_continue_and_after_end_produce_zero_forbidden_broker_writes() {
        let fixture = tempfile::tempdir().unwrap();
        let workspace = fixture.path().join("workspaces");
        std::fs::create_dir(&workspace).unwrap();
        let socket = fixture.path().join("broker.sock");
        let listener = tokio::net::UnixListener::bind(&socket).unwrap();
        let (first_done_tx, first_done_rx) = tokio::sync::oneshot::channel();
        let broker = tokio::spawn(async move {
            let (mut before_continue, _) = listener.accept().await.unwrap();
            broker_handshake_to_request(&mut before_continue).await;
            assert!(matches!(
                read_client_frame(&mut before_continue).await.unwrap(),
                ClientFrame::Cancel
            ));
            first_done_tx.send(()).unwrap();

            let (mut after_end, _) = listener.accept().await.unwrap();
            broker_handshake_to_request(&mut after_end).await;
            write_broker_frame(&mut after_end, &BrokerFrame::Continue)
                .await
                .unwrap();
            assert!(matches!(
                read_client_frame(&mut after_end).await.unwrap(),
                ClientFrame::BodyEnd
            ));
            assert!(matches!(
                read_client_frame(&mut after_end).await.unwrap(),
                ClientFrame::Cancel
            ));
        });
        let proxy = enabled_proxy(&socket, &workspace);

        let first = proxy
            .bind_command(
                "namespace",
                "run",
                "before-continue".to_string(),
                Duration::from_secs(5),
                BashHealth::ready(),
            )
            .unwrap()
            .into_launch(proxy.shell_environment())
            .unwrap();
        let (mut first_local, first_right) = local_session_pair();
        send_control_packet(
            first.control_source.as_raw_fd(),
            &serde_json::to_vec(&packet()).unwrap(),
            &[first_right.as_raw_fd()],
        )
        .unwrap();
        write_local_client_frame(
            &mut first_local,
            &LocalClientFrame::BodyChunk(bytes::Bytes::from_static(b"forbidden")),
        )
        .await
        .unwrap();
        first_done_rx.await.unwrap();
        first.lifecycle.revoke_and_wait().await.unwrap();

        let second = proxy
            .bind_command(
                "namespace",
                "run",
                "after-end".to_string(),
                Duration::from_secs(5),
                BashHealth::ready(),
            )
            .unwrap()
            .into_launch(proxy.shell_environment())
            .unwrap();
        let (mut second_local, second_right) = local_session_pair();
        send_control_packet(
            second.control_source.as_raw_fd(),
            &serde_json::to_vec(&packet()).unwrap(),
            &[second_right.as_raw_fd()],
        )
        .unwrap();
        assert_eq!(
            read_local_runtime_frame(&mut second_local).await.unwrap(),
            LocalRuntimeFrame::Continue
        );
        write_local_client_frame(&mut second_local, &LocalClientFrame::BodyEnd)
            .await
            .unwrap();
        write_local_client_frame(
            &mut second_local,
            &LocalClientFrame::BodyChunk(bytes::Bytes::from_static(b"forbidden")),
        )
        .await
        .unwrap();
        broker.await.unwrap();
        second.lifecycle.revoke_and_wait().await.unwrap();
        assert_eq!(proxy.active_binding_count().unwrap(), 0);
    }

    #[tokio::test]
    async fn check_status_error_response_preserves_output_and_reports_uncommitted_end() {
        let fixture = tempfile::tempdir().unwrap();
        let workspace = fixture.path().join("workspaces");
        let namespace = workspace.join(storage_key("namespace"));
        std::fs::create_dir_all(&namespace).unwrap();
        let destination = namespace.join("result.txt");
        std::fs::write(&destination, b"old-response").unwrap();
        let socket = fixture.path().join("broker.sock");
        let listener = tokio::net::UnixListener::bind(&socket).unwrap();
        let broker = tokio::spawn(async move {
            let (mut stream, _) = listener.accept().await.unwrap();
            broker_handshake_to_request(&mut stream).await;
            write_broker_frame(&mut stream, &BrokerFrame::Continue)
                .await
                .unwrap();
            assert!(matches!(
                read_client_frame(&mut stream).await.unwrap(),
                ClientFrame::BodyEnd
            ));
            write_broker_frame(
                &mut stream,
                &BrokerFrame::ResponseHead(FetchResponseHead {
                    protocol_version: FETCH_PROTOCOL_VERSION,
                    status: 404,
                    reason: "Not Found".to_string(),
                    headers: Vec::new(),
                }),
            )
            .await
            .unwrap();
            write_broker_frame(
                &mut stream,
                &BrokerFrame::ResponseChunk(bytes::Bytes::from_static(b"new-response")),
            )
            .await
            .unwrap();
            write_broker_frame(
                &mut stream,
                &BrokerFrame::ResponseEnd(FetchResponseEnd {
                    protocol_version: FETCH_PROTOCOL_VERSION,
                    body_bytes: 12,
                }),
            )
            .await
            .unwrap();
        });
        let proxy = enabled_proxy(&socket, &workspace);
        let launch = proxy
            .bind_command(
                "namespace",
                "run",
                "check-status-output".to_string(),
                Duration::from_secs(5),
                BashHealth::ready(),
            )
            .unwrap()
            .into_launch(proxy.shell_environment())
            .unwrap();
        let (mut local, transferred) = local_session_pair();
        let mut control = packet();
        control.request.check_status = true;
        control.output_path = Some("/workspace/result.txt".to_string());
        send_control_packet(
            launch.control_source.as_raw_fd(),
            &serde_json::to_vec(&control).unwrap(),
            &[transferred.as_raw_fd()],
        )
        .unwrap();

        assert_eq!(
            read_local_runtime_frame(&mut local).await.unwrap(),
            LocalRuntimeFrame::Continue
        );
        write_local_client_frame(&mut local, &LocalClientFrame::BodyEnd)
            .await
            .unwrap();
        assert!(matches!(
            read_local_runtime_frame(&mut local).await.unwrap(),
            LocalRuntimeFrame::ResponseHead(FetchResponseHead { status: 404, .. })
        ));
        assert!(matches!(
            read_local_runtime_frame(&mut local).await.unwrap(),
            LocalRuntimeFrame::ResponseEnd(end) if !end.output_committed && end.body_bytes == 12
        ));
        broker.await.unwrap();
        launch.lifecycle.revoke_and_wait().await.unwrap();

        assert_eq!(std::fs::read(destination).unwrap(), b"old-response");
        assert!(std::fs::read_dir(namespace).unwrap().all(|entry| {
            !entry
                .unwrap()
                .file_name()
                .to_string_lossy()
                .contains(".tmp")
        }));
    }

    #[tokio::test]
    async fn cli_output_quota_failure_is_one_policy_error_and_exit_65() {
        let fixture = tempfile::tempdir().unwrap();
        let workspace = fixture.path().join("workspaces");
        let namespace = workspace.join(storage_key("namespace"));
        std::fs::create_dir_all(&namespace).unwrap();
        let destination = namespace.join("result.txt");
        std::fs::write(&destination, b"old").unwrap();
        let socket = fixture.path().join("broker.sock");
        let listener = tokio::net::UnixListener::bind(&socket).unwrap();
        let broker = tokio::spawn(async move {
            let (mut stream, _) = listener.accept().await.unwrap();
            broker_handshake_to_request(&mut stream).await;
            write_broker_frame(&mut stream, &BrokerFrame::Continue)
                .await
                .unwrap();
            assert!(matches!(
                read_client_frame(&mut stream).await.unwrap(),
                ClientFrame::BodyEnd
            ));
            write_output_response(&mut stream, b"too-large").await;
        });
        let proxy = enabled_proxy_with_budget(&socket, &workspace, 4);
        let launch = proxy
            .bind_command(
                "namespace",
                "run",
                "quota-output".to_string(),
                Duration::from_secs(5),
                BashHealth::ready(),
            )
            .unwrap()
            .into_launch(proxy.shell_environment())
            .unwrap();
        let child = spawn_fetch(
            &launch.control_source,
            &["--output", "/workspace/result.txt", "https://example.com/"],
        );
        let output = child.wait_with_output().await.unwrap();
        broker.await.unwrap();
        launch.lifecycle.revoke_and_wait().await.unwrap();

        assert_eq!(output.status.code(), Some(65));
        assert_eq!(std::fs::read(destination).unwrap(), b"old");
        assert_no_output_temporary(&namespace);
    }

    #[tokio::test]
    async fn cli_precommit_actual_io_failure_is_one_internal_error_and_exit_70() {
        let fixture = tempfile::tempdir().unwrap();
        let workspace = fixture.path().join("workspaces");
        let namespace = workspace.join(storage_key("namespace"));
        std::fs::create_dir_all(&namespace).unwrap();
        let destination = namespace.join("result.txt");
        std::fs::write(&destination, b"old").unwrap();
        let observed_namespace = namespace.clone();
        let socket = fixture.path().join("broker.sock");
        let listener = tokio::net::UnixListener::bind(&socket).unwrap();
        let broker = tokio::spawn(async move {
            let (mut stream, _) = listener.accept().await.unwrap();
            broker_handshake_to_request(&mut stream).await;
            write_broker_frame(&mut stream, &BrokerFrame::Continue)
                .await
                .unwrap();
            assert!(matches!(
                read_client_frame(&mut stream).await.unwrap(),
                ClientFrame::BodyEnd
            ));
            write_broker_frame(
                &mut stream,
                &BrokerFrame::ResponseHead(FetchResponseHead {
                    protocol_version: FETCH_PROTOCOL_VERSION,
                    status: 200,
                    reason: "OK".to_string(),
                    headers: Vec::new(),
                }),
            )
            .await
            .unwrap();
            write_broker_frame(
                &mut stream,
                &BrokerFrame::ResponseChunk(bytes::Bytes::from_static(b"new-value")),
            )
            .await
            .unwrap();
            let temporary = tokio::time::timeout(Duration::from_secs(1), async {
                loop {
                    if let Some(path) = find_output_temporary(&observed_namespace) {
                        break path;
                    }
                    tokio::task::yield_now().await;
                }
            })
            .await
            .expect("runtime must create an adjacent temporary output");
            std::fs::remove_file(temporary).unwrap();
            write_broker_frame(
                &mut stream,
                &BrokerFrame::ResponseEnd(FetchResponseEnd {
                    protocol_version: FETCH_PROTOCOL_VERSION,
                    body_bytes: 9,
                }),
            )
            .await
            .unwrap();
        });
        let proxy = enabled_proxy_with_budget(&socket, &workspace, 1024);
        let launch = proxy
            .bind_command(
                "namespace",
                "run",
                "io-output".to_string(),
                Duration::from_secs(5),
                BashHealth::ready(),
            )
            .unwrap()
            .into_launch(proxy.shell_environment())
            .unwrap();
        let child = spawn_fetch(
            &launch.control_source,
            &["--output", "/workspace/result.txt", "https://example.com/"],
        );
        let output = child.wait_with_output().await.unwrap();
        broker.await.unwrap();
        launch.lifecycle.revoke_and_wait().await.unwrap();

        assert_eq!(output.status.code(), Some(70));
        assert_eq!(std::fs::read(destination).unwrap(), b"old");
        assert_no_output_temporary(&namespace);
    }

    #[tokio::test]
    async fn runtime_shutdown_uses_owned_revoke_and_join_for_every_binding() {
        let fixture = tempfile::tempdir().unwrap();
        let workspace = fixture.path().join("workspaces");
        std::fs::create_dir(&workspace).unwrap();
        let proxy = agent_runtime::runtime_fetch_proxy::RuntimeFetchProxy::disabled(
            WorkspaceBudget::new(&workspace, 1024).unwrap(),
        );
        let first = proxy
            .bind_command(
                "namespace",
                "run",
                "shutdown-a".to_string(),
                Duration::from_secs(5),
                BashHealth::ready(),
            )
            .unwrap();
        let second = proxy
            .bind_command(
                "namespace",
                "run",
                "shutdown-b".to_string(),
                Duration::from_secs(5),
                BashHealth::ready(),
            )
            .unwrap();
        assert_eq!(proxy.active_binding_count().unwrap(), 2);
        proxy.shutdown().await.unwrap();
        assert_eq!(
            first.lifecycle().phase().unwrap(),
            Some(CommandBindingPhase::Drained)
        );
        assert_eq!(
            second.lifecycle().phase().unwrap(),
            Some(CommandBindingPhase::Drained)
        );
        assert_eq!(proxy.active_binding_count().unwrap(), 0);
    }

    pub(super) async fn scenario_revoke_phase_blocks_admission_before_guardian_drain() {
        let fixture = tempfile::tempdir().unwrap();
        let workspace = fixture.path().join("workspaces");
        std::fs::create_dir(&workspace).unwrap();
        let socket = fixture.path().join("broker.sock");
        let listener = tokio::net::UnixListener::bind(&socket).unwrap();
        let (accepted_tx, mut accepted_rx) = tokio::sync::mpsc::channel(2);
        let broker = tokio::spawn(async move {
            let mut streams = Vec::new();
            while let Ok(Ok((stream, _))) =
                tokio::time::timeout(Duration::from_millis(300), listener.accept()).await
            {
                accepted_tx.send(()).await.unwrap();
                streams.push(stream);
            }
        });
        let proxy = enabled_proxy(&socket, &workspace);
        let launch = proxy
            .bind_command(
                "namespace",
                "run",
                "revoke-order".to_string(),
                Duration::from_secs(5),
                BashHealth::ready(),
            )
            .unwrap()
            .into_launch(proxy.shell_environment())
            .unwrap();
        let (_first_local, first_right) = local_session_pair();
        send_control_packet(
            launch.control_source.as_raw_fd(),
            &serde_json::to_vec(&packet()).unwrap(),
            &[first_right.as_raw_fd()],
        )
        .unwrap();
        accepted_rx.recv().await.unwrap();

        launch.lifecycle.request_revoke();
        assert_eq!(
            launch.lifecycle.phase().unwrap(),
            Some(CommandBindingPhase::Revoked)
        );
        let (_new_local, new_right) = local_session_pair();
        let _ = send_control_packet(
            launch.control_source.as_raw_fd(),
            &serde_json::to_vec(&packet()).unwrap(),
            &[new_right.as_raw_fd()],
        );
        launch.lifecycle.revoke_and_wait().await.unwrap();
        assert_eq!(
            launch.lifecycle.phase().unwrap(),
            Some(CommandBindingPhase::Drained)
        );
        assert_eq!(proxy.active_binding_count().unwrap(), 0);
        assert!(
            tokio::time::timeout(Duration::from_millis(100), accepted_rx.recv())
                .await
                .is_err()
        );
        broker.await.unwrap();
    }

    pub(super) async fn scenario_control_reader_error_receipt_does_not_drop_guardian() {
        let fixture = tempfile::tempdir().unwrap();
        let workspace = fixture.path().join("workspaces");
        std::fs::create_dir(&workspace).unwrap();
        let proxy = agent_runtime::runtime_fetch_proxy::RuntimeFetchProxy::disabled(
            WorkspaceBudget::new(&workspace, 1024).unwrap(),
        );
        let launch = proxy
            .bind_command(
                "namespace",
                "run",
                "control-error".to_string(),
                Duration::from_secs(1),
                BashHealth::ready(),
            )
            .unwrap()
            .into_launch(proxy.shell_environment())
            .unwrap();
        let lifecycle = launch.lifecycle.clone();
        drop(launch.control_source);
        tokio::time::sleep(Duration::from_millis(20)).await;

        let receipt = lifecycle.revoke_and_wait().await.unwrap();

        assert_eq!(receipt.control_reader, ControlReaderOutcome::Error);
        assert_eq!(receipt.guardian.spawned_sessions, 0);
        assert_eq!(receipt.guardian.joined_sessions, 0);
        assert!(receipt.guardian.joinset_empty);
        assert!(receipt.guardian.job_channel_closed);
    }

    pub(super) async fn scenario_runtime_error_is_not_silent_eof_and_terminal_is_never_duplicated()
    {
        let fixture = tempfile::tempdir().unwrap();
        let workspace = fixture.path().join("workspaces");
        std::fs::create_dir(&workspace).unwrap();
        let socket = fixture.path().join("missing-broker.sock");
        let proxy = enabled_proxy(&socket, &workspace);
        let launch = proxy
            .bind_command(
                "namespace",
                "run",
                "terminal-error".to_string(),
                Duration::from_secs(1),
                BashHealth::ready(),
            )
            .unwrap()
            .into_launch(proxy.shell_environment())
            .unwrap();
        let (mut local, transferred) = local_session_pair();
        send_control_packet(
            launch.control_source.as_raw_fd(),
            &serde_json::to_vec(&packet()).unwrap(),
            &[transferred.as_raw_fd()],
        )
        .unwrap();
        drop(transferred);

        let frame = tokio::time::timeout(
            Duration::from_millis(250),
            read_local_runtime_frame(&mut local),
        )
        .await
        .unwrap()
        .unwrap();
        assert!(matches!(
            frame,
            LocalRuntimeFrame::Error(error) if error.code == ErrorCode::Network
        ));
        assert!(
            tokio::time::timeout(
                Duration::from_millis(250),
                read_local_runtime_frame(&mut local),
            )
            .await
            .unwrap()
            .is_err()
        );
        let receipt = launch.lifecycle.revoke_and_wait().await.unwrap();
        assert_eq!(receipt.guardian.spawned_sessions, 1);
        assert_eq!(receipt.guardian.joined_sessions, 1);
        relay_failure_after_response_start_is_one_terminal().await;
        broker_error_then_late_bad_frame_is_one_terminal().await;
    }

    async fn relay_failure_after_response_start_is_one_terminal() {
        let fixture = tempfile::tempdir().unwrap();
        let workspace = fixture.path().join("workspaces");
        std::fs::create_dir(&workspace).unwrap();
        let socket = fixture.path().join("relay-failure.sock");
        let listener = tokio::net::UnixListener::bind(&socket).unwrap();
        let broker = tokio::spawn(async move {
            let (mut stream, _) = listener.accept().await.unwrap();
            broker_handshake_to_request(&mut stream).await;
            write_broker_frame(&mut stream, &BrokerFrame::Continue)
                .await
                .unwrap();
            assert!(matches!(
                read_client_frame(&mut stream).await.unwrap(),
                ClientFrame::BodyEnd
            ));
            write_broker_frame(
                &mut stream,
                &BrokerFrame::ResponseHead(FetchResponseHead {
                    protocol_version: FETCH_PROTOCOL_VERSION,
                    status: 200,
                    reason: "OK".to_string(),
                    headers: Vec::new(),
                }),
            )
            .await
            .unwrap();
            write_broker_frame(
                &mut stream,
                &BrokerFrame::ResponseEnd(FetchResponseEnd {
                    protocol_version: FETCH_PROTOCOL_VERSION,
                    body_bytes: 1,
                }),
            )
            .await
            .unwrap();
        });
        let proxy = enabled_proxy(&socket, &workspace);
        let launch = proxy
            .bind_command(
                "namespace",
                "run",
                "relay-terminal".to_string(),
                Duration::from_secs(1),
                BashHealth::ready(),
            )
            .unwrap()
            .into_launch(proxy.shell_environment())
            .unwrap();
        let (mut local, transferred) = local_session_pair();
        send_control_packet(
            launch.control_source.as_raw_fd(),
            &serde_json::to_vec(&packet()).unwrap(),
            &[transferred.as_raw_fd()],
        )
        .unwrap();
        drop(transferred);
        assert_eq!(
            read_local_runtime_frame(&mut local).await.unwrap(),
            LocalRuntimeFrame::Continue
        );
        write_local_client_frame(&mut local, &LocalClientFrame::BodyEnd)
            .await
            .unwrap();
        assert!(matches!(
            read_local_runtime_frame(&mut local).await.unwrap(),
            LocalRuntimeFrame::ResponseHead(_)
        ));
        assert!(matches!(
            read_local_runtime_frame(&mut local).await.unwrap(),
            LocalRuntimeFrame::Error(error) if error.code == ErrorCode::Protocol
        ));
        assert!(read_local_runtime_frame(&mut local).await.is_err());
        let receipt = launch.lifecycle.revoke_and_wait().await.unwrap();
        assert_eq!(receipt.guardian.spawned_sessions, 1);
        assert_eq!(receipt.guardian.joined_sessions, 1);
        broker.await.unwrap();
    }

    async fn broker_error_then_late_bad_frame_is_one_terminal() {
        let fixture = tempfile::tempdir().unwrap();
        let workspace = fixture.path().join("workspaces");
        std::fs::create_dir(&workspace).unwrap();
        let socket = fixture.path().join("broker-terminal.sock");
        let listener = tokio::net::UnixListener::bind(&socket).unwrap();
        let broker = tokio::spawn(async move {
            let (mut stream, _) = listener.accept().await.unwrap();
            broker_handshake_to_request(&mut stream).await;
            write_broker_frame(&mut stream, &BrokerFrame::Continue)
                .await
                .unwrap();
            assert!(matches!(
                read_client_frame(&mut stream).await.unwrap(),
                ClientFrame::BodyEnd
            ));
            write_broker_frame(
                &mut stream,
                &BrokerFrame::Error(FetchProtocolErrorFrame {
                    protocol_version: FETCH_PROTOCOL_VERSION,
                    code: ErrorCode::Policy,
                    message: "denied".to_string(),
                }),
            )
            .await
            .unwrap();
            let _ = write_broker_frame(&mut stream, &BrokerFrame::ResponseChunk(vec![b'x'].into()))
                .await;
        });
        let proxy = enabled_proxy(&socket, &workspace);
        let launch = proxy
            .bind_command(
                "namespace",
                "run",
                "broker-terminal".to_string(),
                Duration::from_secs(1),
                BashHealth::ready(),
            )
            .unwrap()
            .into_launch(proxy.shell_environment())
            .unwrap();
        let (mut local, transferred) = local_session_pair();
        send_control_packet(
            launch.control_source.as_raw_fd(),
            &serde_json::to_vec(&packet()).unwrap(),
            &[transferred.as_raw_fd()],
        )
        .unwrap();
        drop(transferred);
        assert_eq!(
            read_local_runtime_frame(&mut local).await.unwrap(),
            LocalRuntimeFrame::Continue
        );
        write_local_client_frame(&mut local, &LocalClientFrame::BodyEnd)
            .await
            .unwrap();
        assert!(matches!(
            read_local_runtime_frame(&mut local).await.unwrap(),
            LocalRuntimeFrame::Error(error) if error.code == ErrorCode::Policy
        ));
        assert!(read_local_runtime_frame(&mut local).await.is_err());
        let receipt = launch.lifecycle.revoke_and_wait().await.unwrap();
        assert_eq!(receipt.guardian.spawned_sessions, 1);
        assert_eq!(receipt.guardian.joined_sessions, 1);
        broker.await.unwrap();
    }

    pub(super) async fn scenario_two_active_sessions_reject_third_immediately_and_release_permits_after_join()
     {
        assert_eq!(SESSION_JOB_CHANNEL_CAPACITY, 2);
        let fixture = tempfile::tempdir().unwrap();
        let workspace = fixture.path().join("workspaces");
        std::fs::create_dir(&workspace).unwrap();
        let socket = fixture.path().join("broker.sock");
        let listener = tokio::net::UnixListener::bind(&socket).unwrap();
        let connects = Arc::new(AtomicUsize::new(0));
        let observed = Arc::clone(&connects);
        let (accepted_tx, mut accepted_rx) = tokio::sync::mpsc::channel(2);
        let broker = tokio::spawn(async move {
            let mut streams = Vec::new();
            while let Ok(Ok((stream, _))) =
                tokio::time::timeout(Duration::from_millis(400), listener.accept()).await
            {
                observed.fetch_add(1, Ordering::SeqCst);
                let _ = accepted_tx.send(()).await;
                streams.push(stream);
            }
        });
        let proxy = enabled_proxy(&socket, &workspace);
        let launch = proxy
            .bind_command(
                "namespace",
                "run",
                "session-permits".to_string(),
                Duration::from_secs(2),
                BashHealth::ready(),
            )
            .unwrap()
            .into_launch(proxy.shell_environment())
            .unwrap();
        let mut peers = Vec::new();
        for _ in 0..MAX_ACTIVE_LOCAL_SESSIONS {
            let (local, transferred) = local_session_pair();
            send_control_packet(
                launch.control_source.as_raw_fd(),
                &serde_json::to_vec(&packet()).unwrap(),
                &[transferred.as_raw_fd()],
            )
            .unwrap();
            drop(transferred);
            peers.push(local);
            accepted_rx.recv().await.unwrap();
        }
        let (mut rejected, transferred) = local_session_pair();
        send_control_packet(
            launch.control_source.as_raw_fd(),
            &serde_json::to_vec(&packet()).unwrap(),
            &[transferred.as_raw_fd()],
        )
        .unwrap();
        drop(transferred);
        assert!(
            tokio::time::timeout(
                Duration::from_millis(100),
                read_local_runtime_frame(&mut rejected),
            )
            .await
            .unwrap()
            .is_err()
        );
        assert!(
            tokio::time::timeout(Duration::from_millis(100), accepted_rx.recv())
                .await
                .is_err()
        );
        let receipt = launch.lifecycle.revoke_and_wait().await.unwrap();
        assert_eq!(receipt.guardian.spawned_sessions, 2);
        assert_eq!(receipt.guardian.joined_sessions, 2);
        broker.await.unwrap();
        assert_eq!(connects.load(Ordering::SeqCst), 2);
        drop(peers);
    }

    #[test]
    fn proxy_output_rejects_cross_namespace_and_symlink_destinations() {
        use std::os::unix::fs::symlink;

        let fixture = tempfile::tempdir().unwrap();
        let workspace = fixture.path().join("workspaces");
        let namespace = workspace.join(storage_key("ns"));
        let outside = fixture.path().join("outside");
        std::fs::create_dir_all(&namespace).unwrap();
        std::fs::create_dir(&outside).unwrap();
        std::fs::write(outside.join("victim"), b"outside").unwrap();
        symlink(&outside, namespace.join("escape")).unwrap();
        symlink(outside.join("victim"), namespace.join("target")).unwrap();
        let budget = WorkspaceBudget::new(&workspace, 1024).unwrap();
        let phase = || Arc::new(Mutex::new(CommandBindingPhase::Active));

        assert!(
            OutputCommitGuard::new(&workspace, "ns", "/workspace/escape/new", &budget, phase(),)
                .is_err()
        );
        assert!(
            OutputCommitGuard::new(&workspace, "ns", "/workspace/target", &budget, phase(),)
                .is_err()
        );
        assert_eq!(std::fs::read(outside.join("victim")).unwrap(), b"outside");
    }

    fn enabled_proxy(
        socket: &std::path::Path,
        workspace: &std::path::Path,
    ) -> agent_runtime::runtime_fetch_proxy::RuntimeFetchProxy {
        let security = RuntimeFetchSecurity::new(socket, KEY, limits()).unwrap();
        agent_runtime::runtime_fetch_proxy::RuntimeFetchProxy::enabled(
            security,
            WorkspaceBudget::new(workspace, 1024 * 1024).unwrap(),
        )
    }

    fn enabled_proxy_with_budget(
        socket: &std::path::Path,
        workspace: &std::path::Path,
        max_bytes: u64,
    ) -> agent_runtime::runtime_fetch_proxy::RuntimeFetchProxy {
        let security = RuntimeFetchSecurity::new(socket, KEY, limits()).unwrap();
        agent_runtime::runtime_fetch_proxy::RuntimeFetchProxy::enabled(
            security,
            WorkspaceBudget::new(workspace, max_bytes).unwrap(),
        )
    }

    fn limits() -> FetchClaimLimits {
        FetchClaimLimits {
            protocol_version: FETCH_PROTOCOL_VERSION,
            policy_version: "policy-v1".to_string(),
            max_concurrency: 2,
            max_requests: 20,
            max_request_bytes: 8 * 1024 * 1024,
            max_response_bytes: 32 * 1024 * 1024,
        }
    }

    fn token_verifier() -> TokenVerifier {
        TokenVerifier::new(
            KEY,
            BrokerAuthCaps {
                protocol_version: FETCH_PROTOCOL_VERSION,
                policy_version: "policy-v1".to_string(),
                max_concurrency: 2,
                max_requests: 20,
                max_request_bytes: 8 * 1024 * 1024,
                max_response_bytes: 32 * 1024 * 1024,
                max_future_iat: Duration::from_secs(5),
            },
        )
        .unwrap()
    }

    fn packet() -> CommandControlPacket {
        CommandControlPacket {
            protocol_version: FETCH_PROTOCOL_VERSION,
            request: FetchRequestHead {
                protocol_version: FETCH_PROTOCOL_VERSION,
                method: "GET".to_string(),
                url: "https://example.com/".to_string(),
                headers: Vec::new(),
                follow: false,
                check_status: false,
                timeout_ms: None,
                declared_body_bytes: Some(0),
            },
            output_path: None,
        }
    }

    async fn broker_handshake_to_request(stream: &mut tokio::net::UnixStream) {
        assert!(matches!(
            read_client_frame(stream).await.unwrap(),
            ClientFrame::Hello(_)
        ));
        write_broker_frame(
            stream,
            &BrokerFrame::Hello(BrokerHello {
                protocol_version: FETCH_PROTOCOL_VERSION,
            }),
        )
        .await
        .unwrap();
        assert!(matches!(
            read_client_frame(stream).await.unwrap(),
            ClientFrame::Auth(_)
        ));
        write_broker_frame(stream, &BrokerFrame::Authenticated)
            .await
            .unwrap();
        assert!(matches!(
            read_client_frame(stream).await.unwrap(),
            ClientFrame::Request(_)
        ));
    }

    async fn write_output_response(stream: &mut tokio::net::UnixStream, body: &'static [u8]) {
        write_broker_frame(
            stream,
            &BrokerFrame::ResponseHead(FetchResponseHead {
                protocol_version: FETCH_PROTOCOL_VERSION,
                status: 200,
                reason: "OK".to_string(),
                headers: Vec::new(),
            }),
        )
        .await
        .unwrap();
        write_broker_frame(
            stream,
            &BrokerFrame::ResponseChunk(bytes::Bytes::from_static(body)),
        )
        .await
        .unwrap();
        write_broker_frame(
            stream,
            &BrokerFrame::ResponseEnd(FetchResponseEnd {
                protocol_version: FETCH_PROTOCOL_VERSION,
                body_bytes: body.len() as u64,
            }),
        )
        .await
        .unwrap();
    }

    fn spawn_fetch(control: &OwnedFd, args: &[&str]) -> tokio::process::Child {
        let duplicate = unsafe { libc::fcntl(control.as_raw_fd(), libc::F_DUPFD_CLOEXEC, 10) };
        assert!(duplicate >= 10, "{}", io::Error::last_os_error());
        let inherited = unsafe { OwnedFd::from_raw_fd(duplicate) };
        let mut command = tokio::process::Command::new(env!("CARGO_BIN_EXE_fetch"));
        command
            .args(args)
            .env_clear()
            .env("AGENT_FETCH_CONTROL_FD", "4")
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        unsafe {
            command.pre_exec(move || {
                if libc::dup2(inherited.as_raw_fd(), 4) == -1
                    || libc::fcntl(4, libc::F_SETFD, 0) == -1
                {
                    return Err(io::Error::last_os_error());
                }
                Ok(())
            });
        }
        command.spawn().unwrap()
    }

    fn find_output_temporary(directory: &std::path::Path) -> Option<std::path::PathBuf> {
        std::fs::read_dir(directory)
            .ok()?
            .filter_map(Result::ok)
            .find(|entry| entry.file_name().to_string_lossy().contains(".tmp"))
            .map(|entry| entry.path())
    }

    fn assert_no_output_temporary(directory: &std::path::Path) {
        assert!(find_output_temporary(directory).is_none());
    }

    fn local_session_pair() -> (tokio::net::UnixStream, std::os::unix::net::UnixStream) {
        let (local, transferred) = std::os::unix::net::UnixStream::pair().unwrap();
        local.set_nonblocking(true).unwrap();
        (
            tokio::net::UnixStream::from_std(local).unwrap(),
            transferred,
        )
    }

    fn send_control_packet(control: i32, payload: &[u8], descriptors: &[i32]) -> io::Result<()> {
        let mut iovec = libc::iovec {
            iov_base: payload.as_ptr().cast_mut().cast(),
            iov_len: payload.len(),
        };
        let mut ancillary = [MaybeUninit::<libc::cmsghdr>::uninit(); 64];
        let mut message: libc::msghdr = unsafe { std::mem::zeroed() };
        message.msg_iov = &mut iovec;
        message.msg_iovlen = 1;
        if !descriptors.is_empty() {
            let data_bytes = descriptors.len() * size_of::<i32>();
            message.msg_control = ancillary.as_mut_ptr().cast();
            message.msg_controllen = unsafe { libc::CMSG_SPACE(data_bytes as u32) as usize };
            let header = unsafe { libc::CMSG_FIRSTHDR(&message) };
            unsafe {
                (*header).cmsg_level = libc::SOL_SOCKET;
                (*header).cmsg_type = libc::SCM_RIGHTS;
                (*header).cmsg_len = libc::CMSG_LEN(data_bytes as u32) as usize;
                std::ptr::copy_nonoverlapping(
                    descriptors.as_ptr(),
                    libc::CMSG_DATA(header).cast::<i32>(),
                    descriptors.len(),
                );
            }
        }
        let sent = unsafe { libc::sendmsg(control, &message, libc::MSG_NOSIGNAL) };
        if sent < 0 {
            Err(io::Error::last_os_error())
        } else if sent as usize != payload.len() {
            Err(io::Error::new(
                io::ErrorKind::WriteZero,
                "short control packet",
            ))
        } else {
            Ok(())
        }
    }
}

#[cfg(target_os = "linux")]
#[tokio::test]
async fn c7_revoke_phase_blocks_admission_before_guardian_drain() {
    linux_proxy::scenario_revoke_phase_blocks_admission_before_guardian_drain().await;
}

#[cfg(target_os = "linux")]
#[tokio::test]
async fn c7_control_reader_error_receipt_does_not_drop_guardian() {
    linux_proxy::scenario_control_reader_error_receipt_does_not_drop_guardian().await;
}

#[cfg(target_os = "linux")]
#[tokio::test]
async fn c7_runtime_error_is_not_silent_eof_and_terminal_is_never_duplicated() {
    linux_proxy::scenario_runtime_error_is_not_silent_eof_and_terminal_is_never_duplicated().await;
    #[cfg(feature = "c7-test-support")]
    {
        let receipt = agent_runtime::c7_test_support::terminal_guardian_evidence().await;
        assert!(receipt.broker_error_then_internal_exactly_once);
        assert!(receipt.writer_unavailable_no_frame);
        assert!(receipt.valid_guardian_exact);
        assert!(receipt.panic_guardian_rejected);
        assert!(receipt.unclassified_guardian_rejected);
    }
}

#[cfg(target_os = "linux")]
#[tokio::test]
async fn c7_two_active_sessions_reject_third_immediately_and_release_permits_after_join() {
    linux_proxy::scenario_two_active_sessions_reject_third_immediately_and_release_permits_after_join().await;
}

#[cfg(all(target_os = "linux", feature = "c7-test-support"))]
#[tokio::test]
async fn c7_control_reader_panic_still_drains_guardian() {
    let receipt = agent_runtime::c7_test_support::control_panic_receipt().await;
    assert_eq!(
        receipt.drain.control_reader,
        agent_runtime::runtime_fetch_proxy::ControlReaderOutcome::Panicked
    );
    assert_eq!(receipt.live_sessions_before_panic, 2);
    assert_eq!(receipt.permits_while_live, 0);
    assert_eq!(receipt.drain.guardian.spawned_sessions, 2);
    assert_eq!(receipt.drain.guardian.joined_sessions, 2);
    assert!(receipt.drain.guardian.joinset_empty);
    assert!(receipt.drain.guardian.job_channel_closed);
    assert_eq!(receipt.permits_after_receipts, 2);
    assert!(receipt.cleanup_authorized);
}

#[cfg(all(target_os = "linux", feature = "c7-test-support"))]
#[tokio::test]
async fn c7_guardian_receipt_mismatch_blocks_cgroup_cleanup() {
    let receipt = agent_runtime::c7_test_support::guardian_mismatch_receipt().await;
    assert!(matches!(
        receipt.error,
        agent_runtime::runtime_fetch_proxy::BindingDrainError::ReceiptMismatch
    ));
    assert_eq!(receipt.phase, CommandBindingPhase::Revoked);
    assert_eq!(receipt.registry_entries, 1);
    assert!(!receipt.health_ready);
}

#[cfg(all(target_os = "linux", feature = "c7-test-support"))]
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn c7_guardian_timeout_retains_entry_handles_and_joinset() {
    let receipt = agent_runtime::c7_test_support::guardian_timeout_receipt().await;
    assert!(matches!(
        receipt.error,
        agent_runtime::runtime_fetch_proxy::BindingDrainError::DrainPending
    ));
    assert!(receipt.same_handle_retained);
    assert_eq!(receipt.phase_before_release, CommandBindingPhase::Revoked);
    assert_eq!(receipt.registry_before_release, 1);
    assert_eq!(receipt.live_sessions_before_revoke, 2);
    assert_eq!(receipt.live_sessions_after_timeout, 2);
    assert_eq!(receipt.permits_before_revoke, 0);
    assert_eq!(receipt.permits_after_timeout, 0);
    assert!(!receipt.cleanup_authorized_before_release);
    assert_eq!(receipt.guardian_spawned_after_shutdown, 2);
    assert_eq!(receipt.guardian_joined_after_shutdown, 2);
    assert!(receipt.guardian_joinset_empty_after_shutdown);
    assert_eq!(receipt.permits_after_shutdown, 2);
    assert_eq!(receipt.live_sessions_after_shutdown, 0);
    assert_eq!(receipt.registry_after_shutdown, 0);
}

#[cfg(all(target_os = "linux", feature = "c7-test-support"))]
#[tokio::test(flavor = "multi_thread")]
async fn c7_deferred_cgroup_cleanup_waits_for_shutdown_drain_receipt() {
    let receipt = agent_runtime::c7_test_support::deferred_cleanup_receipt().await;
    assert!(receipt.request_bounded);
    assert!(receipt.cleanup_blocked_before_receipt);
    assert!(receipt.cleanup_completed_after_shutdown);
    assert!(receipt.markers_empty);
}

#[cfg(all(target_os = "linux", feature = "c7-test-support"))]
#[tokio::test]
async fn c7_lifecycle_trace_orders_drain_before_cleanup_complete() {
    let receipt = agent_runtime::c7_test_support::trace_receipt(false).await;
    assert_eq!(
        receipt.events,
        [
            "command_binding_owned_drain_complete",
            "command_cgroup_cleanup_complete"
        ]
    );
    assert!(receipt.health_ready);
    assert!(!receipt.cleanup_failed);
}

#[cfg(all(target_os = "linux", feature = "c7-test-support"))]
#[tokio::test]
async fn c7_cgroup_cleanup_failure_omits_complete_trace_and_latches_health() {
    let receipt = agent_runtime::c7_test_support::trace_receipt(true).await;
    assert_eq!(receipt.events, ["command_binding_owned_drain_complete"]);
    assert!(!receipt.health_ready);
    assert!(receipt.cleanup_failed);
}

#[cfg(all(target_os = "linux", feature = "c7-test-support"))]
#[tokio::test]
async fn c7_output_capacity_path_and_busy_send_one_policy_terminal_exit_65() {
    let receipt = agent_runtime::c7_test_support::policy_terminal_receipt().await;
    assert!(receipt.exactly_once);
    assert_eq!(receipt.codes, [ErrorCode::Policy; 3]);
    for code in receipt.codes {
        assert_eq!(
            agent_runtime::c7_test_support::exit_for_error_code(code),
            65
        );
    }
}

#[cfg(all(target_os = "linux", feature = "c7-test-support"))]
#[tokio::test]
async fn c7_output_open_write_file_sync_and_rename_send_one_internal_terminal_exit_70() {
    let receipt = agent_runtime::c7_test_support::internal_terminal_receipt().await;
    assert!(receipt.exactly_once);
    assert_eq!(receipt.codes, [ErrorCode::Internal; 4]);
    for code in receipt.codes {
        assert_eq!(
            agent_runtime::c7_test_support::exit_for_error_code(code),
            70
        );
    }
}

#[cfg(all(target_os = "linux", feature = "c7-test-support"))]
#[tokio::test]
async fn c7_pre_rename_failure_preserves_old_file_and_returns_70() {
    let receipt = agent_runtime::c7_test_support::pre_rename_receipt().await;
    assert_eq!(receipt.code, ErrorCode::Internal);
    assert_eq!(
        agent_runtime::c7_test_support::exit_for_error_code(receipt.code),
        70
    );
    assert!(receipt.old_preserved);
    assert!(receipt.temporary_absent);
}

#[cfg(all(target_os = "linux", feature = "c7-test-support"))]
#[test]
fn c7_post_rename_directory_sync_failure_is_committed_and_latches_shared_health() {
    let receipt = agent_runtime::c7_test_support::post_rename_receipt();
    assert!(receipt.committed);
    assert!(receipt.new_visible);
    assert!(!receipt.health_ready);
    assert_eq!(
        receipt.health_reason,
        "bash unavailable: workspace durability failed"
    );
}

#[cfg(all(target_os = "linux", feature = "c7-test-support"))]
#[tokio::test]
async fn c7_command_binding_uses_supervisor_bash_health() {
    let receipt = agent_runtime::c7_test_support::shared_supervisor_health_receipt().await;
    assert!(!receipt.supervisor_ready);
    assert!(!receipt.binding_ready);
    assert!(receipt.same_reason);
}

fn request() -> FetchRequestHead {
    FetchRequestHead {
        protocol_version: FETCH_PROTOCOL_VERSION,
        method: "GET".to_string(),
        url: "https://example.com".to_string(),
        headers: Vec::new(),
        follow: false,
        check_status: false,
        timeout_ms: None,
        declared_body_bytes: None,
    }
}

#[test]
fn output_commit_and_revoke_are_linearized_by_the_binding_phase() {
    let root = tempfile::tempdir().unwrap();
    let namespace_root = root.path().join(storage_key("ns"));
    std::fs::create_dir(&namespace_root).unwrap();
    let budget = WorkspaceBudget::new(root.path(), 32).unwrap();

    let revoked_phase = Arc::new(Mutex::new(CommandBindingPhase::Active));
    let mut revoked = OutputCommitGuard::new(
        root.path(),
        "ns",
        "/workspace/revoked.txt",
        &budget,
        Arc::clone(&revoked_phase),
    )
    .unwrap();
    revoked.write_chunk(b"never-commit").unwrap();
    *revoked_phase.lock().unwrap() = CommandBindingPhase::Revoked;
    assert!(revoked.commit_if_active().is_err());
    assert!(!namespace_root.join("revoked.txt").exists());
    assert!(std::fs::read_dir(&namespace_root).unwrap().all(|entry| {
        !entry
            .unwrap()
            .file_name()
            .to_string_lossy()
            .contains(".tmp")
    }));

    let committed_phase = Arc::new(Mutex::new(CommandBindingPhase::Active));
    let mut committed = OutputCommitGuard::new(
        root.path(),
        "ns",
        "/workspace/committed.txt",
        &budget,
        Arc::clone(&committed_phase),
    )
    .unwrap();
    committed.write_chunk(b"stable").unwrap();
    committed.commit_if_active().unwrap();
    *committed_phase.lock().unwrap() = CommandBindingPhase::Revoked;
    assert_eq!(
        std::fs::read(namespace_root.join("committed.txt")).unwrap(),
        b"stable"
    );
}

#[test]
fn output_budget_failure_preserves_old_file_and_removes_temporary() {
    let root = tempfile::tempdir().unwrap();
    let namespace_root = root.path().join(storage_key("ns"));
    std::fs::create_dir(&namespace_root).unwrap();
    let destination = namespace_root.join("result.txt");
    std::fs::write(&destination, b"old").unwrap();
    let budget = WorkspaceBudget::new(root.path(), 4).unwrap();
    let phase = Arc::new(Mutex::new(CommandBindingPhase::Active));
    let mut output =
        OutputCommitGuard::new(root.path(), "ns", "/workspace/result.txt", &budget, phase).unwrap();
    assert!(output.write_chunk(b"too-large").is_err());
    drop(output);

    assert_eq!(std::fs::read(destination).unwrap(), b"old");
    assert!(std::fs::read_dir(namespace_root).unwrap().all(|entry| {
        !entry
            .unwrap()
            .file_name()
            .to_string_lossy()
            .contains(".tmp")
    }));
}

#[test]
fn output_path_must_be_a_literal_path_in_the_bound_workspace() {
    let root = tempfile::tempdir().unwrap();
    let budget = WorkspaceBudget::new(root.path(), 32).unwrap();
    for path in [
        "/workspace/../other/file",
        "/workspace2/file",
        "workspace/file",
        "/tmp/file",
        "/workspace\\escape",
    ] {
        assert!(
            OutputCommitGuard::new(
                root.path(),
                "ns",
                path,
                &budget,
                Arc::new(Mutex::new(CommandBindingPhase::Active)),
            )
            .is_err(),
            "{path}"
        );
    }
}

#[test]
fn command_packet_is_identity_free_bounded_metadata() {
    let packet = CommandControlPacket {
        protocol_version: FETCH_PROTOCOL_VERSION,
        request: request(),
        output_path: Some("/workspace/result.bin".to_string()),
    };
    let json = serde_json::to_vec(&packet).unwrap();
    assert!(json.len() <= MAX_COMMAND_CONTROL_PACKET_BYTES);
    let text = String::from_utf8(json).unwrap();
    for forbidden in ["token", "namespace", "run_id", "command_id", "socket"] {
        assert!(!text.contains(forbidden));
    }
    assert_eq!(LOCAL_SESSION_CHANNEL_CAPACITY, 1);
}

#[test]
fn strict_local_request_and_response_states_reject_duplicates_and_reordering() {
    let mut request = LocalRequestState::default();
    assert!(request.body_chunk(1).is_err());
    request.continued().unwrap();
    request.body_chunk(65_536).unwrap();
    assert!(request.body_chunk(65_537).is_err());
    request.body_end().unwrap();
    assert!(request.body_end().is_err());
    assert!(request.body_chunk(1).is_err());

    let mut response = LocalResponseState::default();
    assert!(response.response_chunk(1).is_err());
    response.response_head().unwrap();
    assert!(response.response_head().is_err());
    response.response_chunk(1).unwrap();
    response.response_end().unwrap();
    assert!(response.response_chunk(1).is_err());
}
