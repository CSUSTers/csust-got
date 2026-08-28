#[cfg(feature = "c7-test-support")]
use super::super::{RuntimeFetchProxyError, RuntimeFetchProxyErrorCategory};
#[cfg(feature = "c7-test-support")]
use std::sync::Arc;

#[derive(Clone)]
pub(in crate::runtime_fetch_proxy) enum SessionFault {
    #[cfg(test)]
    Panic,
    #[cfg(test)]
    Uncategorized,
    #[cfg(test)]
    Pending,
    #[cfg(feature = "c7-test-support")]
    PendingWithSignal(Arc<tokio::sync::Notify>),
    #[cfg(feature = "c7-test-support")]
    Blocking(BlockingSessionFault),
    #[cfg(feature = "c7-test-support")]
    PanicWithSignal(Arc<tokio::sync::Notify>),
    #[cfg(feature = "c7-test-support")]
    UncategorizedWithSignal(Arc<tokio::sync::Notify>),
}

#[cfg(feature = "c7-test-support")]
#[derive(Clone)]
pub(in crate::runtime_fetch_proxy) struct BlockingSessionFault {
    pub(in crate::runtime_fetch_proxy) started: Arc<tokio::sync::Notify>,
    pub(in crate::runtime_fetch_proxy) live: Arc<std::sync::atomic::AtomicUsize>,
    pub(in crate::runtime_fetch_proxy) release: Arc<(std::sync::Mutex<bool>, std::sync::Condvar)>,
}

#[cfg(feature = "c7-test-support")]
impl BlockingSessionFault {
    pub(in crate::runtime_fetch_proxy) fn wait(&self) -> Result<(), RuntimeFetchProxyError> {
        use std::sync::atomic::Ordering;
        self.live.fetch_add(1, Ordering::SeqCst);
        self.started.notify_one();
        let result = (|| {
            let released =
                self.release.0.lock().map_err(|_| {
                    RuntimeFetchProxyError::new("session test barrier is unavailable")
                })?;
            let (released, timeout) = self
                .release
                .1
                .wait_timeout_while(released, std::time::Duration::from_secs(3), |released| {
                    !*released
                })
                .map_err(|_| RuntimeFetchProxyError::new("session test barrier is unavailable"))?;
            if timeout.timed_out() && !*released {
                return Err(RuntimeFetchProxyError::with_category(
                    RuntimeFetchProxyErrorCategory::Timeout,
                    "session test barrier timed out",
                ));
            }
            Ok(())
        })();
        self.live.fetch_sub(1, Ordering::SeqCst);
        result
    }
}
