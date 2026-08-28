use super::server::BrokerMetrics;
use std::{
    sync::{
        Arc,
        atomic::{AtomicU64, Ordering},
    },
    time::Duration,
};
use tokio::{
    sync::{OwnedSemaphorePermit, Semaphore},
    time::Instant,
};

pub(crate) struct PreAuthAdmission {
    _permit: OwnedSemaphorePermit,
    active: Arc<AtomicU64>,
    deadline: Instant,
}

impl PreAuthAdmission {
    pub(crate) fn try_acquire(
        semaphore: &Arc<Semaphore>,
        metrics: &BrokerMetrics,
        accepted_at: Instant,
        handshake_timeout: Duration,
    ) -> Option<Self> {
        let permit = match Arc::clone(semaphore).try_acquire_owned() {
            Ok(permit) => permit,
            Err(_) => {
                metrics.rejected_pre_auth.fetch_add(1, Ordering::Relaxed);
                return None;
            }
        };
        metrics.active_pre_auth.fetch_add(1, Ordering::Relaxed);
        Some(Self {
            _permit: permit,
            active: Arc::clone(&metrics.active_pre_auth),
            deadline: accepted_at + handshake_timeout,
        })
    }

    pub(crate) fn deadline(&self) -> Instant {
        self.deadline
    }
}

impl Drop for PreAuthAdmission {
    fn drop(&mut self) {
        self.active.fetch_sub(1, Ordering::Relaxed);
    }
}
