use super::types::{CommandIdentity, VerifiedClaims, duration_seconds, unix_seconds};
use std::{
    collections::HashMap,
    fmt,
    sync::{Arc, Mutex, Weak},
    time::{Duration, SystemTime},
};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum QuotaErrorKind {
    Expired,
    ConcurrencyLimitReached,
    RequestLimitReached,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct QuotaError {
    kind: QuotaErrorKind,
}

impl QuotaError {
    fn new(kind: QuotaErrorKind) -> Self {
        Self { kind }
    }

    pub fn kind(&self) -> QuotaErrorKind {
        self.kind
    }
}

impl fmt::Display for QuotaError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(match self.kind {
            QuotaErrorKind::Expired => "fetch quota has expired",
            QuotaErrorKind::ConcurrencyLimitReached => "fetch concurrency quota exceeded",
            QuotaErrorKind::RequestLimitReached => "fetch request quota exceeded",
        })
    }
}

impl std::error::Error for QuotaError {}

#[derive(Clone)]
pub struct QuotaRegistry {
    cleanup_window: Duration,
    state: Arc<Mutex<QuotaState>>,
}

struct QuotaState {
    entries: HashMap<CommandIdentity, QuotaEntry>,
}

struct QuotaEntry {
    expires_at_unix: i64,
    concurrency: u16,
    requests: u16,
}

impl QuotaRegistry {
    pub fn new(cleanup_window: Duration) -> Self {
        Self {
            cleanup_window,
            state: Arc::new(Mutex::new(QuotaState {
                entries: HashMap::new(),
            })),
        }
    }

    pub fn acquire(&self, claims: &VerifiedClaims) -> Result<QuotaLease, QuotaError> {
        self.acquire_at(claims, SystemTime::now())
    }

    pub fn acquire_at(
        &self,
        claims: &VerifiedClaims,
        now: SystemTime,
    ) -> Result<QuotaLease, QuotaError> {
        let now = unix_seconds(now).map_err(|_| QuotaError::new(QuotaErrorKind::Expired))?;
        if claims.claims.expires_at_unix <= now {
            return Err(QuotaError::new(QuotaErrorKind::Expired));
        }

        let mut state = self.state.lock().expect("quota registry mutex poisoned");
        cleanup_entries(&mut state.entries, now, self.cleanup_window);
        let entry = state
            .entries
            .entry(claims.identity.clone())
            .or_insert(QuotaEntry {
                expires_at_unix: claims.claims.expires_at_unix,
                concurrency: 0,
                requests: 0,
            });
        entry.expires_at_unix = entry.expires_at_unix.max(claims.claims.expires_at_unix);

        if entry.concurrency >= claims.effective_limits.max_concurrency {
            return Err(QuotaError::new(QuotaErrorKind::ConcurrencyLimitReached));
        }
        if entry.requests >= claims.effective_limits.max_requests {
            return Err(QuotaError::new(QuotaErrorKind::RequestLimitReached));
        }
        entry.concurrency = entry.concurrency.saturating_add(1);
        entry.requests = entry.requests.saturating_add(1);

        Ok(QuotaLease {
            state: Arc::downgrade(&self.state),
            identity: claims.identity.clone(),
            requests_used: entry.requests,
            concurrent_requests: entry.concurrency,
        })
    }

    pub fn cleanup_at(&self, now: SystemTime) -> usize {
        let Ok(now) = unix_seconds(now) else {
            return 0;
        };
        let mut state = self.state.lock().expect("quota registry mutex poisoned");
        cleanup_entries(&mut state.entries, now, self.cleanup_window)
    }

    pub fn entry_count_at(&self, now: SystemTime) -> usize {
        self.cleanup_at(now);
        self.state
            .lock()
            .expect("quota registry mutex poisoned")
            .entries
            .len()
    }
}

impl fmt::Debug for QuotaRegistry {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str("QuotaRegistry([REDACTED])")
    }
}

pub struct QuotaLease {
    state: Weak<Mutex<QuotaState>>,
    identity: CommandIdentity,
    requests_used: u16,
    concurrent_requests: u16,
}

impl QuotaLease {
    pub fn requests_used(&self) -> u16 {
        self.requests_used
    }

    pub fn concurrent_requests(&self) -> u16 {
        self.concurrent_requests
    }
}

impl Drop for QuotaLease {
    fn drop(&mut self) {
        let Some(state) = self.state.upgrade() else {
            return;
        };
        let mut state = state.lock().expect("quota registry mutex poisoned");
        if let Some(entry) = state.entries.get_mut(&self.identity) {
            entry.concurrency = entry.concurrency.saturating_sub(1);
        }
    }
}

impl fmt::Debug for QuotaLease {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str("QuotaLease([REDACTED])")
    }
}

fn cleanup_entries(
    entries: &mut HashMap<CommandIdentity, QuotaEntry>,
    now_unix: i64,
    cleanup_window: Duration,
) -> usize {
    let cleanup_seconds = duration_seconds(cleanup_window);
    let initial_count = entries.len();
    entries.retain(|_, entry| {
        entry.concurrency > 0 || now_unix <= entry.expires_at_unix.saturating_add(cleanup_seconds)
    });
    initial_count - entries.len()
}
