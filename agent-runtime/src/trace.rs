#[cfg(test)]
use std::path::Path;
use std::{io, path::PathBuf, sync::Arc};

use tokio::{
    fs,
    io::{AsyncWrite, AsyncWriteExt},
    sync::Mutex,
};

#[derive(Clone)]
pub struct JsonlTraceSink {
    path: Arc<PathBuf>,
    lock: Arc<Mutex<()>>,
}

impl JsonlTraceSink {
    pub fn new(path: PathBuf) -> Self {
        Self {
            path: Arc::new(path),
            lock: Arc::new(Mutex::new(())),
        }
    }

    pub async fn append_record(&self, payload_with_newline: Vec<u8>) -> io::Result<()> {
        if !has_exactly_one_terminal_newline(&payload_with_newline) {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "trace record must contain exactly one terminal newline",
            ));
        }

        let _guard = self.lock.lock().await;
        if let Some(parent) = self.path.parent() {
            fs::create_dir_all(parent).await?;
        }
        let mut file = fs::OpenOptions::new()
            .create(true)
            .append(true)
            .open(self.path.as_path())
            .await?;
        #[cfg(test)]
        return append_runtime_trace_record_with_writer(&mut file, &payload_with_newline, None)
            .await;
        #[cfg(not(test))]
        append_runtime_trace_record_with_writer(&mut file, &payload_with_newline).await
    }

    #[cfg(test)]
    pub(crate) fn path(&self) -> &Path {
        self.path.as_path()
    }
}

fn has_exactly_one_terminal_newline(payload: &[u8]) -> bool {
    payload.len() >= 2 && payload[payload.len() - 1] == b'\n' && payload[payload.len() - 2] != b'\n'
}

#[cfg(test)]
struct TraceWriteCheckpoint {
    entered: std::sync::atomic::AtomicUsize,
    release: tokio::sync::Semaphore,
}

#[cfg(test)]
impl TraceWriteCheckpoint {
    fn new() -> Self {
        Self {
            entered: std::sync::atomic::AtomicUsize::new(0),
            release: tokio::sync::Semaphore::new(0),
        }
    }

    async fn after_record(&self) {
        self.entered
            .fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        self.release
            .acquire()
            .await
            .expect("trace write checkpoint must remain available")
            .forget();
    }

    fn release_all(&self, count: usize) {
        self.release.add_permits(count);
    }
}

async fn append_runtime_trace_record_with_writer<W: AsyncWrite + Unpin>(
    writer: &mut W,
    payload_with_newline: &[u8],
    #[cfg(test)] checkpoint: Option<&TraceWriteCheckpoint>,
) -> io::Result<()> {
    writer.write_all(payload_with_newline).await?;
    #[cfg(test)]
    if let Some(checkpoint) = checkpoint {
        checkpoint.after_record().await;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{
        pin::Pin,
        sync::Mutex as StdMutex,
        task::{Context, Poll},
        time::Duration,
    };
    use tokio::time::timeout;

    #[derive(Clone)]
    struct BarrierTraceWriter {
        bytes: Arc<StdMutex<Vec<u8>>>,
    }

    impl BarrierTraceWriter {
        fn new(bytes: Arc<StdMutex<Vec<u8>>>) -> Self {
            Self { bytes }
        }
    }

    impl AsyncWrite for BarrierTraceWriter {
        fn poll_write(
            self: Pin<&mut Self>,
            _cx: &mut Context<'_>,
            buf: &[u8],
        ) -> Poll<io::Result<usize>> {
            self.bytes.lock().unwrap().extend_from_slice(buf);
            Poll::Ready(Ok(buf.len()))
        }

        fn poll_flush(self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<io::Result<()>> {
            Poll::Ready(Ok(()))
        }

        fn poll_shutdown(self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<io::Result<()>> {
            Poll::Ready(Ok(()))
        }
    }

    fn assert_complete_trace_jsonl(bytes: &[u8], expected_count: usize) {
        let lines = bytes
            .split(|byte| *byte == b'\n')
            .filter(|line| !line.is_empty())
            .collect::<Vec<_>>();
        assert_eq!(lines.len(), expected_count);
        let mut ids = std::collections::BTreeSet::new();
        for line in lines {
            let value: serde_json::Value = serde_json::from_slice(line).unwrap();
            let id = value["id"].as_u64().unwrap();
            assert!(ids.insert(id), "duplicate trace record id {id}");
        }
        assert_eq!(ids.len(), expected_count);
        assert_eq!(ids, (0..expected_count as u64).collect());
    }

    #[tokio::test]
    async fn trace_sink_forced_payload_newline_interleave_is_indivisible() {
        let bytes = Arc::new(StdMutex::new(Vec::new()));
        let checkpoint = Arc::new(TraceWriteCheckpoint::new());
        let mut writes = Vec::new();
        for id in 0..2 {
            let bytes = bytes.clone();
            let checkpoint = checkpoint.clone();
            writes.push(tokio::spawn(async move {
                let mut writer = BarrierTraceWriter::new(bytes);
                let mut payload = serde_json::to_vec(&serde_json::json!({ "id": id })).unwrap();
                payload.push(b'\n');
                append_runtime_trace_record_with_writer(&mut writer, &payload, Some(&checkpoint))
                    .await
            }));
        }
        timeout(Duration::from_secs(1), async {
            while checkpoint.entered.load(std::sync::atomic::Ordering::SeqCst) != 2 {
                tokio::task::yield_now().await;
            }
        })
        .await
        .expect("both complete records must reach the checkpoint");
        checkpoint.release_all(2);
        for write in writes {
            write.await.unwrap().unwrap();
        }

        let bytes = bytes.lock().unwrap().clone();
        assert_complete_trace_jsonl(&bytes, 2);
    }

    #[tokio::test]
    async fn trace_sink_concurrent_records_are_indivisible() {
        let temp = tempfile::tempdir().unwrap();
        let sink = JsonlTraceSink::new(temp.path().join("traces/trace.jsonl"));
        let mut writes = Vec::new();
        for id in 0..128 {
            let sink = sink.clone();
            writes.push(tokio::spawn(async move {
                let mut payload = serde_json::to_vec(&serde_json::json!({ "id": id })).unwrap();
                payload.push(b'\n');
                sink.append_record(payload).await
            }));
        }
        for write in writes {
            write.await.unwrap().unwrap();
        }

        let bytes = fs::read(sink.path()).await.unwrap();
        assert_complete_trace_jsonl(&bytes, 128);
    }

    #[tokio::test]
    async fn trace_sink_rejects_missing_or_repeated_terminal_newlines() {
        let temp = tempfile::tempdir().unwrap();
        let sink = JsonlTraceSink::new(temp.path().join("trace.jsonl"));
        for payload in [b"{}".as_slice(), b"{}\n\n".as_slice()] {
            let error = sink.append_record(payload.to_vec()).await.unwrap_err();
            assert_eq!(error.kind(), io::ErrorKind::InvalidInput);
        }
    }
}
