use std::{
    future::Future,
    pin::Pin,
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
    task::{Context, Poll},
};

mod grep;
mod read;
#[cfg(test)]
mod tests;

pub use grep::{
    GREP_MAX_ELAPSED, GREP_MAX_ENTRIES, GREP_MAX_SCANNED_BYTES, ScanBudget, ScanLimits,
    finish_bounded_text, grep_reader_bounded,
};
pub use read::{
    BoundedText, READ_CHUNK_BYTES, TRUNCATION_MARKER, read_text_bounded,
    read_text_bounded_cancellable,
};

pub struct CancellableBlocking<T> {
    cancel: Arc<AtomicBool>,
    task: tokio::task::JoinHandle<T>,
}

pub fn spawn_cancellable_blocking<T, F>(operation: F) -> CancellableBlocking<T>
where
    T: Send + 'static,
    F: FnOnce(Arc<AtomicBool>) -> T + Send + 'static,
{
    let cancel = Arc::new(AtomicBool::new(false));
    let worker_cancel = Arc::clone(&cancel);
    let task = tokio::task::spawn_blocking(move || operation(worker_cancel));
    CancellableBlocking { cancel, task }
}

impl<T> Future for CancellableBlocking<T> {
    type Output = Result<T, tokio::task::JoinError>;

    fn poll(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        let this = self.get_mut();
        Pin::new(&mut this.task).poll(cx)
    }
}

impl<T> Drop for CancellableBlocking<T> {
    fn drop(&mut self) {
        self.cancel.store(true, Ordering::Release);
    }
}
