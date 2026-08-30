use super::transport::{ConnectError, ResponseStream};
use async_compression::tokio::bufread::{BrotliDecoder, GzipDecoder, ZlibDecoder};
use bytes::Bytes;
use futures_util::StreamExt as _;
use std::{
    io,
    pin::Pin,
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, AtomicU64, Ordering},
    },
};
use tokio::{
    io::BufReader,
    io::{AsyncBufRead, AsyncRead, AsyncReadExt as _, AsyncWrite},
};
use tokio_util::io::StreamReader;

use crate::{
    fetch_policy::BudgetTracker,
    fetch_protocol::{BrokerFrame, ProtocolError, write_broker_frame},
};

const STREAM_CHUNK_BYTES: usize = 64 * 1024;

#[derive(Debug)]
pub(crate) enum StreamError {
    Policy,
    Upstream,
    Output(ProtocolError),
}

#[derive(Default)]
pub(crate) struct ResponseProgress {
    network_bytes: AtomicU64,
    decoded_bytes: AtomicU64,
}

impl ResponseProgress {
    fn record_network(&self, bytes: u64) {
        self.network_bytes.fetch_add(bytes, Ordering::Relaxed);
    }

    fn record_decoded(&self, bytes: u64) {
        self.decoded_bytes.fetch_add(bytes, Ordering::Relaxed);
    }

    pub(crate) fn snapshot(&self) -> (u64, u64) {
        (
            self.network_bytes.load(Ordering::Relaxed),
            self.decoded_bytes.load(Ordering::Relaxed),
        )
    }
}

pub(crate) async fn forward_response_body<W>(
    body: ResponseStream,
    encoding: Option<&str>,
    budgets: BudgetTracker,
    progress: Arc<ResponseProgress>,
    writer: &mut W,
) -> Result<(u64, u64), StreamError>
where
    W: AsyncWrite + Unpin,
{
    let budgets = Arc::new(Mutex::new(budgets));
    let rejected = Arc::new(AtomicBool::new(false));
    let raw = metered_stream(
        body,
        Arc::clone(&budgets),
        Arc::clone(&rejected),
        Arc::clone(&progress),
    );
    let reader = BufReader::new(StreamReader::new(raw));
    let mut reader = decoder(reader, encoding)?;
    let mut buffer = [0_u8; STREAM_CHUNK_BYTES];
    loop {
        let read = reader.read(&mut buffer).await.map_err(|_| {
            if rejected.load(Ordering::Acquire) {
                StreamError::Policy
            } else {
                StreamError::Upstream
            }
        })?;
        if read == 0 {
            break;
        }
        progress.record_decoded(read as u64);
        let decoded_ok = {
            let mut tracker = budgets.lock().map_err(|_| StreamError::Policy)?;
            tracker.record_response_decoded_chunk(read as u64).is_ok()
        };
        if !decoded_ok {
            return Err(StreamError::Policy);
        }
        write_broker_frame(
            writer,
            &BrokerFrame::ResponseChunk(Bytes::copy_from_slice(&buffer[..read])),
        )
        .await
        .map_err(StreamError::Output)?;
    }
    Ok(progress.snapshot())
}

fn metered_stream(
    body: ResponseStream,
    budgets: Arc<Mutex<BudgetTracker>>,
    rejected: Arc<AtomicBool>,
    progress: Arc<ResponseProgress>,
) -> impl futures_util::Stream<Item = Result<Bytes, io::Error>> + Send {
    body.map(move |item| match item {
        Ok(chunk) => {
            progress.record_network(chunk.len() as u64);
            let result = budgets.lock().ok().and_then(|mut tracker| {
                tracker
                    .record_response_network_chunk(chunk.len() as u64)
                    .ok()
            });
            if result.is_some() {
                Ok(chunk)
            } else {
                rejected.store(true, Ordering::Release);
                Err(io::Error::other("response network budget exceeded"))
            }
        }
        Err(ConnectError::Timeout) => {
            Err(io::Error::new(io::ErrorKind::TimedOut, "upstream timeout"))
        }
        Err(_) => Err(io::Error::other("upstream response failed")),
    })
}

fn decoder<R>(
    reader: R,
    encoding: Option<&str>,
) -> Result<Pin<Box<dyn AsyncRead + Send>>, StreamError>
where
    R: AsyncBufRead + Unpin + Send + 'static,
{
    match encoding.map(str::trim).filter(|value| !value.is_empty()) {
        None | Some("identity") => Ok(Box::pin(reader)),
        Some("gzip") => Ok(Box::pin(GzipDecoder::new(reader))),
        Some("deflate") => Ok(Box::pin(ZlibDecoder::new(reader))),
        Some("br") => Ok(Box::pin(BrotliDecoder::new(reader))),
        Some(_) => Err(StreamError::Policy),
    }
}
