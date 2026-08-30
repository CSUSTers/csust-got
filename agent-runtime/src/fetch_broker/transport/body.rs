use crate::fetch_policy::{ApprovedTarget, ReviewedHeaders, ReviewedTarget};
use async_trait::async_trait;
use bytes::Bytes;
use futures_util::Stream;
use http::{HeaderMap, Method, StatusCode};
use std::{
    fmt,
    net::IpAddr,
    pin::Pin,
    sync::{
        Arc,
        atomic::{AtomicU64, Ordering},
    },
    task::{Context, Poll},
};
use tokio::sync::mpsc;

pub(crate) const UPLOAD_QUEUE_CAPACITY: usize = 4;
pub type ResponseStream = Pin<Box<dyn Stream<Item = Result<Bytes, ConnectError>> + Send>>;

#[derive(Default)]
pub(crate) struct BodyQueueMetrics {
    queued_frames: AtomicU64,
    max_queued_frames: AtomicU64,
}

impl BodyQueueMetrics {
    pub(crate) fn queued_frames(&self) -> u64 {
        self.queued_frames.load(Ordering::Relaxed)
    }

    pub(crate) fn max_queued_frames(&self) -> u64 {
        self.max_queued_frames.load(Ordering::Relaxed)
    }

    fn queued(&self) {
        let queued = self.queued_frames.fetch_add(1, Ordering::Relaxed) + 1;
        self.max_queued_frames.fetch_max(queued, Ordering::Relaxed);
    }

    fn dequeued(&self) {
        let _ = self
            .queued_frames
            .fetch_update(Ordering::Relaxed, Ordering::Relaxed, |queued| {
                Some(queued.saturating_sub(1))
            });
    }
}

pub(crate) struct BodySender {
    sender: mpsc::Sender<Bytes>,
    metrics: Arc<BodyQueueMetrics>,
}

impl BodySender {
    pub(crate) async fn send(&self, chunk: Bytes) -> Result<(), ()> {
        let permit = self.sender.reserve().await.map_err(|_| ())?;
        self.metrics.queued();
        permit.send(chunk);
        Ok(())
    }
}

pub struct BodyStream {
    receiver: mpsc::Receiver<Bytes>,
    metrics: Arc<BodyQueueMetrics>,
}

impl BodyStream {
    pub fn empty() -> Self {
        let metrics = Arc::new(BodyQueueMetrics::default());
        let (sender, receiver) = mpsc::channel(1);
        drop(sender);
        Self { receiver, metrics }
    }
}

impl Stream for BodyStream {
    type Item = Result<Bytes, ConnectError>;

    fn poll_next(mut self: Pin<&mut Self>, context: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        match self.receiver.poll_recv(context) {
            Poll::Ready(Some(chunk)) => {
                self.metrics.dequeued();
                Poll::Ready(Some(Ok(chunk)))
            }
            Poll::Ready(None) => Poll::Ready(None),
            Poll::Pending => Poll::Pending,
        }
    }
}

impl Drop for BodyStream {
    fn drop(&mut self) {
        let pending = self.receiver.len() as u64;
        let _ = self.metrics.queued_frames.fetch_update(
            Ordering::Relaxed,
            Ordering::Relaxed,
            |queued| Some(queued.saturating_sub(pending)),
        );
    }
}

pub(crate) fn body_channel(metrics: Arc<BodyQueueMetrics>) -> (BodySender, BodyStream) {
    let (sender, receiver) = mpsc::channel(UPLOAD_QUEUE_CAPACITY);
    (
        BodySender {
            sender,
            metrics: Arc::clone(&metrics),
        },
        BodyStream { receiver, metrics },
    )
}

#[derive(Clone, Debug)]
pub struct ReviewedRequest {
    pub method: Method,
    pub target: ReviewedTarget,
    pub headers: ReviewedHeaders,
}

pub struct UpstreamResponse {
    pub status: StatusCode,
    pub reason: String,
    pub headers: HeaderMap,
    pub body: ResponseStream,
}

impl UpstreamResponse {
    pub fn from_chunks<I>(status: StatusCode, headers: HeaderMap, chunks: I) -> Self
    where
        I: IntoIterator<Item = Result<Bytes, ConnectError>>,
        I::IntoIter: Send + 'static,
    {
        Self {
            status,
            reason: status.canonical_reason().unwrap_or_default().to_string(),
            headers,
            body: Box::pin(futures_util::stream::iter(chunks)),
        }
    }
}

impl fmt::Debug for UpstreamResponse {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter
            .debug_struct("UpstreamResponse")
            .field("status", &self.status)
            .field("reason", &self.reason)
            .field("headers", &self.headers)
            .finish_non_exhaustive()
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum ResolveError {
    Timeout,
    Failed,
}

impl fmt::Display for ResolveError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(match self {
            Self::Timeout => "DNS resolution timed out",
            Self::Failed => "DNS resolution failed",
        })
    }
}

impl std::error::Error for ResolveError {}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum ConnectError {
    Timeout,
    PeerMismatch,
    Failed,
}

impl fmt::Display for ConnectError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(match self {
            Self::Timeout => "upstream connection timed out",
            Self::PeerMismatch => "upstream peer did not match the reviewed address",
            Self::Failed => "upstream connection failed",
        })
    }
}

impl std::error::Error for ConnectError {}

#[async_trait]
pub trait Resolver: Send + Sync {
    async fn resolve_all(&self, host: &str) -> Result<Vec<IpAddr>, ResolveError>;
}

#[async_trait]
pub trait PinnedConnector: Send + Sync {
    async fn execute(
        &self,
        request: ReviewedRequest,
        target: ApprovedTarget,
        body: BodyStream,
    ) -> Result<UpstreamResponse, ConnectError>;
}
