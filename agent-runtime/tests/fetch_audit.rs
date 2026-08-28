use agent_runtime::audit::{
    AuditBodyDigest, AuditCancellationReason, AuditCompletion, AuditHealth, AuditQuotaUse,
    AuditRedirect, AuditSensitiveHeader, AuditSink, AuditStart, AuditWriter, JsonlAuditSink,
};
use serde::de::{self, DeserializeSeed, MapAccess, SeqAccess, Visitor};
use serde_json::Value;
use sha2::{Digest, Sha256};
use std::{
    io,
    net::IpAddr,
    sync::{Arc, Mutex},
    time::Duration,
};
use tempfile::tempdir;

#[tokio::test]
async fn jsonl_audit_persists_redacted_start_and_completion_records() {
    let root = tempdir().unwrap();
    let path = root.path().join("fetch.jsonl");
    let sink = JsonlAuditSink::open(&path).await.unwrap();

    let transaction = AuditSink::begin(&sink, audit_start()).await.unwrap();
    AuditSink::complete(&sink, transaction, body_completion())
        .await
        .unwrap();

    assert_eq!(sink.health(), AuditHealth::Healthy);

    let contents = std::fs::read_to_string(path).unwrap();
    for secret in [
        "namespace-secret",
        "run-secret",
        "command-secret",
        "query-secret",
        "body-secret",
        "Bearer header-secret",
        "Cookie=session-secret",
        "redirect-secret",
    ] {
        assert!(!contents.contains(secret), "audit contained {secret:?}");
    }

    let records: Vec<Value> = contents
        .lines()
        .map(serde_json::from_str)
        .collect::<Result<_, _>>()
        .unwrap();
    assert_eq!(records.len(), 2);

    let start = &records[0];
    assert_eq!(start["event"], "start");
    assert_eq!(start["method"], "POST");
    assert_eq!(start["normalized_origin"], "https://public.example:8443");
    assert_eq!(start["query_byte_len"], 20);
    assert_eq!(start["request_body_byte_len"], 0);
    assert_eq!(start["namespace_sha256"].as_str().unwrap().len(), 64);
    assert_eq!(start["run_id_sha256"].as_str().unwrap().len(), 64);
    assert_eq!(start["command_id_sha256"].as_str().unwrap().len(), 64);
    assert_eq!(
        start["query_sha256"].as_str().unwrap(),
        sha256_hex(b"api_key=query-secret")
    );
    assert_eq!(
        start["request_body_sha256"].as_str().unwrap(),
        sha256_hex(b"")
    );
    assert_eq!(start["sensitive_headers"][0]["byte_len"], 20);
    assert_eq!(
        start["sensitive_headers"][0]["sha256"]
            .as_str()
            .unwrap()
            .len(),
        64
    );
    assert_eq!(start["policy_version"], "policy-v1");

    let completion = &records[1];
    assert_eq!(completion["event"], "completion");
    assert_eq!(completion["status"], 201);
    assert_eq!(completion["approved_ip"], "93.184.216.34");
    assert_eq!(
        completion["redirect_chain"][0]["normalized_origin"],
        "https://redirect.example"
    );
    assert_eq!(completion["network_bytes"], 512);
    assert_eq!(completion["decoded_bytes"], 768);
    assert_eq!(completion["request_body_bytes"], 11);
    assert_eq!(
        completion["request_body_sha256"],
        sha256_hex(b"body-secret")
    );
    assert_eq!(completion["duration_ms"], 25);
    assert_eq!(completion["quota"]["requests_used"], 3);
    assert_eq!(completion["cancellation_reason"], "broken_pipe");
}

#[tokio::test]
async fn start_and_completion_raw_jsonl_have_unique_object_keys() {
    let records = Arc::new(Mutex::new(Vec::new()));
    let sink = JsonlAuditSink::with_writer(RecordingWriter {
        records: Arc::clone(&records),
    });
    let transaction = sink.begin(audit_start()).await.unwrap();
    sink.complete(transaction, body_completion()).await.unwrap();

    let records = records.lock().unwrap();
    let start = std::str::from_utf8(&records[0]).unwrap();
    let completion = std::str::from_utf8(&records[1]).unwrap();
    assert_eq!(raw_key_count(start, "request_body_byte_len"), 1);
    assert_eq!(raw_key_count(start, "request_body_sha256"), 1);
    assert_eq!(raw_key_count(completion, "request_body_byte_len"), 1);
    assert_eq!(raw_key_count(completion, "request_body_bytes"), 1);
    assert_eq!(
        raw_key_count(completion, "request_body_sha256"),
        1,
        "raw Completion JSON must contain exactly one final request_body_sha256: {completion}"
    );
    assert_unique_object_keys(start);
    assert_unique_object_keys(completion);

    let start: Value = serde_json::from_str(start).unwrap();
    let completion: Value = serde_json::from_str(completion).unwrap();
    assert_eq!(start["request_body_byte_len"], 0);
    assert_eq!(start["request_body_sha256"], sha256_hex(b""));
    assert_eq!(completion["request_body_byte_len"], 0);
    assert_eq!(completion["request_body_bytes"], 11);
    assert_eq!(
        completion["request_body_sha256"],
        sha256_hex(b"body-secret")
    );
}

#[tokio::test]
async fn begin_write_failure_rejects_the_request() {
    let state = Arc::new(Mutex::new(WriterState::failing_on(1)));
    let sink = JsonlAuditSink::with_writer(ControlledWriter::new(Arc::clone(&state)));

    let error = sink.begin(audit_start()).await.unwrap_err();

    assert_eq!(error.to_string(), "audit record could not be persisted");
    assert_eq!(sink.health(), AuditHealth::Unhealthy);
    assert_eq!(state.lock().unwrap().calls, 1);
}

#[tokio::test]
async fn completion_write_failure_latches_unhealthy_and_rejects_next_begin() {
    let state = Arc::new(Mutex::new(WriterState::failing_on(2)));
    let sink = JsonlAuditSink::with_writer(ControlledWriter::new(Arc::clone(&state)));
    let transaction = sink.begin(audit_start()).await.unwrap();

    let error = sink
        .complete(transaction, successful_completion())
        .await
        .unwrap_err();

    assert_eq!(error.to_string(), "audit record could not be persisted");
    assert_eq!(sink.health(), AuditHealth::Unhealthy);
    assert_eq!(state.lock().unwrap().calls, 2);

    let next_begin = sink.begin(audit_start()).await.unwrap_err();
    assert_eq!(next_begin.to_string(), "audit sink is unhealthy");
    assert_eq!(state.lock().unwrap().calls, 2);
}

fn audit_start() -> AuditStart {
    AuditStart::new(
        "namespace-secret",
        "run-secret",
        "command-secret",
        "POST",
        "https://public.example:8443/request?api_key=query-secret",
        b"api_key=query-secret",
        b"",
        [
            AuditSensitiveHeader::new("Authorization", b"Bearer header-secret"),
            AuditSensitiveHeader::new("Cookie", b"Cookie=session-secret"),
        ],
        "policy-v1",
    )
}

fn body_completion() -> AuditCompletion {
    AuditCompletion {
        status: Some(201),
        approved_ip: Some("93.184.216.34".parse::<IpAddr>().unwrap()),
        redirect_chain: vec![AuditRedirect::new(
            "https://redirect.example/next?redirect_token=redirect-secret",
            "93.184.216.35".parse().unwrap(),
        )],
        network_bytes: 512,
        decoded_bytes: 768,
        request_body_bytes: 11,
        request_body_sha256: sha256_hex(b"body-secret"),
        duration: Duration::from_millis(25),
        quota: AuditQuotaUse {
            requests_used: 3,
            concurrent_requests: 1,
            request_bytes_used: 11,
            response_bytes_used: 768,
        },
        rejection_reason: None,
        cancellation_reason: Some(AuditCancellationReason::BrokenPipe),
    }
}

fn successful_completion() -> AuditCompletion {
    AuditCompletion::new(
        Some(200),
        Some("93.184.216.34".parse().unwrap()),
        Vec::new(),
        1,
        1,
        AuditBodyDigest::empty(),
        Duration::from_millis(1),
        AuditQuotaUse::default(),
    )
}

fn sha256_hex(value: &[u8]) -> String {
    let mut hasher = Sha256::new();
    hasher.update(value);
    hasher
        .finalize()
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

struct ControlledWriter {
    state: Arc<Mutex<WriterState>>,
}

impl ControlledWriter {
    fn new(state: Arc<Mutex<WriterState>>) -> Self {
        Self { state }
    }
}

impl AuditWriter for ControlledWriter {
    fn append(&mut self, _serialized_record: &[u8]) -> io::Result<()> {
        let mut state = self.state.lock().unwrap();
        state.calls += 1;
        if state.fail_on_call == Some(state.calls) {
            return Err(io::Error::other("injected audit failure"));
        }
        Ok(())
    }
}

struct RecordingWriter {
    records: Arc<Mutex<Vec<Vec<u8>>>>,
}

impl AuditWriter for RecordingWriter {
    fn append(&mut self, serialized_record: &[u8]) -> io::Result<()> {
        self.records
            .lock()
            .unwrap()
            .push(serialized_record.to_vec());
        Ok(())
    }
}

fn raw_key_count(raw: &str, key: &str) -> usize {
    raw.match_indices(&format!("\"{key}\":")).count()
}

fn assert_unique_object_keys(raw: &str) {
    let mut deserializer = serde_json::Deserializer::from_str(raw);
    UniqueObjectKeys.deserialize(&mut deserializer).unwrap();
    deserializer.end().unwrap();
}

struct UniqueObjectKeys;

impl<'de> DeserializeSeed<'de> for UniqueObjectKeys {
    type Value = ();

    fn deserialize<D>(self, deserializer: D) -> Result<Self::Value, D::Error>
    where
        D: serde::Deserializer<'de>,
    {
        deserializer.deserialize_any(UniqueObjectKeysVisitor)
    }
}

struct UniqueObjectKeysVisitor;

impl<'de> Visitor<'de> for UniqueObjectKeysVisitor {
    type Value = ();

    fn expecting(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter.write_str("JSON with unique keys in every object")
    }

    fn visit_bool<E>(self, _: bool) -> Result<Self::Value, E> {
        Ok(())
    }
    fn visit_i64<E>(self, _: i64) -> Result<Self::Value, E> {
        Ok(())
    }
    fn visit_u64<E>(self, _: u64) -> Result<Self::Value, E> {
        Ok(())
    }
    fn visit_f64<E>(self, _: f64) -> Result<Self::Value, E> {
        Ok(())
    }
    fn visit_str<E>(self, _: &str) -> Result<Self::Value, E> {
        Ok(())
    }
    fn visit_none<E>(self) -> Result<Self::Value, E> {
        Ok(())
    }
    fn visit_unit<E>(self) -> Result<Self::Value, E> {
        Ok(())
    }

    fn visit_seq<A>(self, mut sequence: A) -> Result<Self::Value, A::Error>
    where
        A: SeqAccess<'de>,
    {
        while sequence.next_element_seed(UniqueObjectKeys)?.is_some() {}
        Ok(())
    }

    fn visit_map<A>(self, mut object: A) -> Result<Self::Value, A::Error>
    where
        A: MapAccess<'de>,
    {
        let mut keys = std::collections::HashSet::new();
        while let Some(key) = object.next_key::<String>()? {
            if !keys.insert(key.clone()) {
                return Err(de::Error::custom(format!("duplicate object key {key:?}")));
            }
            object.next_value_seed(UniqueObjectKeys)?;
        }
        Ok(())
    }
}

struct WriterState {
    calls: usize,
    fail_on_call: Option<usize>,
}

impl WriterState {
    fn failing_on(call: usize) -> Self {
        Self {
            calls: 0,
            fail_on_call: Some(call),
        }
    }
}
