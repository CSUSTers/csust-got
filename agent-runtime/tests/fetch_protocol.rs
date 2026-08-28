use agent_runtime::fetch_protocol::{
    AuthMetadata, BrokerFrame, BrokerHello, ClientFrame, ClientHello, ErrorCode, FetchProbe,
    FetchProtocolErrorFrame, FetchReady, FetchRequestHead, FetchResponseEnd, FetchResponseHead,
    LocalClientFrame, LocalResponseEnd, LocalRuntimeFrame, MAX_BODY_FRAME_BYTES,
    MAX_ERROR_TEXT_BYTES, MAX_METADATA_BYTES, ProtocolError, SecretString, read_broker_frame,
    read_client_frame, read_local_client_frame, read_local_runtime_frame, write_broker_frame,
    write_client_frame, write_local_client_frame, write_local_runtime_frame,
};
use bytes::Bytes;
use tokio::io::{AsyncWriteExt as _, duplex};

#[tokio::test]
async fn fragmented_control_frame_round_trips() {
    let frame = ClientFrame::Request(FetchRequestHead {
        protocol_version: 1,
        method: "PATCH".to_string(),
        url: "https://example.com/items/1".to_string(),
        headers: vec![("x-test".to_string(), "yes".to_string())],
        follow: true,
        check_status: false,
        timeout_ms: Some(2_000),
        declared_body_bytes: Some(3),
    });
    let (mut encoded_writer, mut encoded_reader) = duplex(MAX_METADATA_BYTES + 16);
    write_client_frame(&mut encoded_writer, &frame)
        .await
        .unwrap();
    encoded_writer.shutdown().await.unwrap();
    let mut encoded = Vec::new();
    tokio::io::AsyncReadExt::read_to_end(&mut encoded_reader, &mut encoded)
        .await
        .unwrap();

    let (mut writer, mut reader) = duplex(7);
    let sending = tokio::spawn(async move {
        for byte in encoded {
            writer.write_all(&[byte]).await.unwrap();
        }
    });
    let decoded = read_client_frame(&mut reader).await.unwrap();
    sending.await.unwrap();
    assert_eq!(decoded, frame);
}

#[tokio::test]
async fn local_codec_preserves_frames_without_coalescing() {
    let client = LocalClientFrame::BodyChunk(Bytes::from(vec![7; MAX_BODY_FRAME_BYTES]));
    let (mut writer, mut reader) = duplex(MAX_BODY_FRAME_BYTES + 5);
    write_local_client_frame(&mut writer, &client)
        .await
        .unwrap();
    assert_eq!(read_local_client_frame(&mut reader).await.unwrap(), client);

    let runtime = LocalRuntimeFrame::Continue;
    let (mut writer, mut reader) = duplex(16);
    write_local_runtime_frame(&mut writer, &runtime)
        .await
        .unwrap();
    assert_eq!(
        read_local_runtime_frame(&mut reader).await.unwrap(),
        runtime
    );

    let runtime = LocalRuntimeFrame::ResponseEnd(LocalResponseEnd {
        protocol_version: 1,
        body_bytes: 42,
        output_committed: true,
    });
    let (mut writer, mut reader) = duplex(MAX_METADATA_BYTES + 5);
    write_local_runtime_frame(&mut writer, &runtime)
        .await
        .unwrap();
    assert_eq!(
        read_local_runtime_frame(&mut reader).await.unwrap(),
        runtime
    );
}

#[tokio::test]
async fn local_codec_rejects_declared_65537_before_reading_payload() {
    let encoded = [vec![0x21], 65_537_u32.to_be_bytes().to_vec()].concat();
    let mut input = encoded.as_slice();
    assert!(matches!(
        read_local_client_frame(&mut input).await,
        Err(ProtocolError::FrameTooLarge {
            size: 65_537,
            limit: 65_536
        })
    ));
}

#[tokio::test]
async fn every_protocol_frame_round_trips_without_body_coalescing() {
    let client_frames = [
        ClientFrame::Hello(ClientHello {
            protocol_version: 1,
        }),
        ClientFrame::Auth(AuthMetadata {
            protocol_version: 1,
            token: SecretString::new("top-secret"),
        }),
        ClientFrame::BodyChunk(Bytes::from_static(b"abc")),
        ClientFrame::BodyEnd,
        ClientFrame::Cancel,
        ClientFrame::Probe(FetchProbe {
            protocol_version: 1,
            policy_version: "policy-v1".to_string(),
            nonce: [7; 16],
            mac: [8; 32],
        }),
    ];
    for expected in client_frames {
        let (mut writer, mut reader) = duplex(MAX_BODY_FRAME_BYTES + 16);
        write_client_frame(&mut writer, &expected).await.unwrap();
        assert_eq!(read_client_frame(&mut reader).await.unwrap(), expected);
    }

    let broker_frames = [
        BrokerFrame::Hello(BrokerHello {
            protocol_version: 1,
        }),
        BrokerFrame::Authenticated,
        BrokerFrame::Continue,
        BrokerFrame::ResponseHead(FetchResponseHead {
            protocol_version: 1,
            status: 200,
            reason: "OK".to_string(),
            headers: vec![("content-type".to_string(), "text/plain".to_string())],
        }),
        BrokerFrame::ResponseChunk(Bytes::from_static(b"xyz")),
        BrokerFrame::ResponseEnd(FetchResponseEnd {
            protocol_version: 1,
            body_bytes: 3,
        }),
        BrokerFrame::Error(FetchProtocolErrorFrame {
            protocol_version: 1,
            code: ErrorCode::Policy,
            message: "denied".to_string(),
        }),
        BrokerFrame::Ready(FetchReady {
            protocol_version: 1,
            policy_version: "policy-v1".to_string(),
            nonce: [7; 16],
            mac: [9; 32],
        }),
    ];
    for expected in broker_frames {
        let (mut writer, mut reader) = duplex(MAX_BODY_FRAME_BYTES + 16);
        write_broker_frame(&mut writer, &expected).await.unwrap();
        assert_eq!(read_broker_frame(&mut reader).await.unwrap(), expected);
    }
}

#[tokio::test]
async fn unknown_kinds_lengths_versions_and_metadata_totals_fail_closed() {
    let cases = [
        vec![0xff, 0, 0, 0, 0],
        [
            vec![1],
            ((MAX_METADATA_BYTES + 1) as u32).to_be_bytes().to_vec(),
        ]
        .concat(),
    ];
    for bytes in cases {
        let mut input = bytes.as_slice();
        assert!(matches!(
            read_client_frame(&mut input).await,
            Err(ProtocolError::UnknownFrame(_)) | Err(ProtocolError::FrameTooLarge { .. })
        ));
    }

    let (mut writer, mut reader) = duplex(128);
    let unsupported = br#"{"protocol_version":99}"#;
    writer.write_all(&[1]).await.unwrap();
    writer
        .write_all(&(unsupported.len() as u32).to_be_bytes())
        .await
        .unwrap();
    writer.write_all(unsupported).await.unwrap();
    assert!(matches!(
        read_client_frame(&mut reader).await,
        Err(ProtocolError::UnsupportedVersion(99))
    ));

    let oversized_headers = FetchRequestHead {
        protocol_version: 1,
        method: "GET".to_string(),
        url: "https://example.com".to_string(),
        headers: vec![("x-large".to_string(), "x".repeat(MAX_METADATA_BYTES))],
        follow: false,
        check_status: false,
        timeout_ms: None,
        declared_body_bytes: None,
    };
    let mut sink = tokio::io::sink();
    assert!(matches!(
        write_client_frame(&mut sink, &ClientFrame::Request(oversized_headers)).await,
        Err(ProtocolError::MetadataTooLarge { .. })
    ));
}

#[tokio::test]
async fn body_and_error_bounds_are_enforced() {
    let mut sink = tokio::io::sink();
    let body = ClientFrame::BodyChunk(Bytes::from(vec![0; MAX_BODY_FRAME_BYTES + 1]));
    assert!(matches!(
        write_client_frame(&mut sink, &body).await,
        Err(ProtocolError::FrameTooLarge { .. })
    ));

    let error = BrokerFrame::Error(FetchProtocolErrorFrame {
        protocol_version: 1,
        code: ErrorCode::Internal,
        message: "x".repeat(MAX_ERROR_TEXT_BYTES + 1),
    });
    assert!(matches!(
        write_broker_frame(&mut sink, &error).await,
        Err(ProtocolError::ErrorTextTooLarge { .. })
    ));
}

#[test]
fn secrets_are_redacted_from_debug() {
    let token = SecretString::new("never-print-this");
    assert_eq!(token.expose_secret(), "never-print-this");
    assert!(!format!("{token:?}").contains("never-print-this"));
    let frame = ClientFrame::Auth(AuthMetadata {
        protocol_version: 1,
        token,
    });
    assert!(!format!("{frame:?}").contains("never-print-this"));
}
