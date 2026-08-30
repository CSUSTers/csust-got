use super::records::{
    AuditRecord, AuditTransaction, CompletionAuditRecord, CompletionAuditRequest,
};
use super::{AuditCompletion, AuditStart};
use serde::Serialize;
use std::{
    future::Future,
    io::{self, Write},
    path::Path,
    pin::Pin,
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, Ordering},
    },
};

pub type AuditFuture<'a, T> = Pin<Box<dyn Future<Output = Result<T, AuditError>> + Send + 'a>>;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum AuditHealth {
    Healthy,
    Unhealthy,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum AuditError {
    Unhealthy,
    WriteFailed,
}

impl std::fmt::Display for AuditError {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Unhealthy => formatter.write_str("audit sink is unhealthy"),
            Self::WriteFailed => formatter.write_str("audit record could not be persisted"),
        }
    }
}

impl std::error::Error for AuditError {}

pub trait AuditSink: Send + Sync {
    fn begin(&self, start: AuditStart) -> AuditFuture<'_, AuditTransaction>;
    fn complete(
        &self,
        transaction: AuditTransaction,
        completion: AuditCompletion,
    ) -> AuditFuture<'_, ()>;
    fn health(&self) -> AuditHealth;
}

pub trait AuditWriter: Send {
    fn append(&mut self, serialized_record: &[u8]) -> io::Result<()>;
}

#[derive(Clone)]
pub struct JsonlAuditSink {
    state: Arc<AuditState>,
}

struct AuditState {
    writer: Mutex<Box<dyn AuditWriter>>,
    healthy: AtomicBool,
}

struct FileAuditWriter {
    file: std::fs::File,
}

impl AuditWriter for FileAuditWriter {
    fn append(&mut self, serialized_record: &[u8]) -> io::Result<()> {
        self.file.write_all(serialized_record)?;
        self.file.write_all(b"\n")?;
        self.file.flush()?;
        self.file.sync_data()
    }
}

impl JsonlAuditSink {
    pub async fn open(path: impl AsRef<Path>) -> Result<Self, AuditError> {
        let path = path.as_ref().to_path_buf();
        let file = tokio::task::spawn_blocking(move || {
            std::fs::OpenOptions::new()
                .create(true)
                .append(true)
                .open(path)
        })
        .await
        .map_err(|_| AuditError::WriteFailed)?
        .map_err(|_| AuditError::WriteFailed)?;
        Ok(Self::with_writer(FileAuditWriter { file }))
    }

    pub fn with_writer<W>(writer: W) -> Self
    where
        W: AuditWriter + 'static,
    {
        Self {
            state: Arc::new(AuditState {
                writer: Mutex::new(Box::new(writer)),
                healthy: AtomicBool::new(true),
            }),
        }
    }

    pub async fn begin(&self, start: AuditStart) -> Result<AuditTransaction, AuditError> {
        let sink = self.clone();
        tokio::task::spawn_blocking(move || sink.begin_record(start))
            .await
            .map_err(|_| self.latch_unhealthy(AuditError::WriteFailed))?
    }

    pub async fn complete(
        &self,
        transaction: AuditTransaction,
        completion: AuditCompletion,
    ) -> Result<(), AuditError> {
        let sink = self.clone();
        tokio::task::spawn_blocking(move || sink.complete_record(transaction, completion))
            .await
            .map_err(|_| self.latch_unhealthy(AuditError::WriteFailed))?
    }

    pub fn health(&self) -> AuditHealth {
        if self.state.healthy.load(Ordering::Acquire) {
            AuditHealth::Healthy
        } else {
            AuditHealth::Unhealthy
        }
    }

    fn begin_record(&self, start: AuditStart) -> Result<AuditTransaction, AuditError> {
        self.ensure_healthy()?;
        let transaction = AuditTransaction {
            identity: start.identity,
            request: start.request,
        };
        let record = AuditRecord::Start {
            identity: &transaction.identity,
            request: &transaction.request,
        };
        self.persist(&record)
            .map_err(|error| self.latch_unhealthy(error))?;
        Ok(transaction)
    }

    fn complete_record(
        &self,
        transaction: AuditTransaction,
        completion: AuditCompletion,
    ) -> Result<(), AuditError> {
        self.ensure_healthy()?;
        let record = AuditRecord::Completion {
            identity: &transaction.identity,
            request: CompletionAuditRequest::from(&transaction.request),
            completion: CompletionAuditRecord::from(completion),
        };
        self.persist(&record)
            .map_err(|error| self.latch_unhealthy(error))
    }

    fn ensure_healthy(&self) -> Result<(), AuditError> {
        match self.health() {
            AuditHealth::Healthy => Ok(()),
            AuditHealth::Unhealthy => Err(AuditError::Unhealthy),
        }
    }

    fn persist(&self, record: &impl Serialize) -> Result<(), AuditError> {
        let serialized = serde_json::to_vec(record).map_err(|_| AuditError::WriteFailed)?;
        let mut writer = self
            .state
            .writer
            .lock()
            .map_err(|_| AuditError::WriteFailed)?;
        self.ensure_healthy()?;
        writer
            .append(&serialized)
            .map_err(|_| AuditError::WriteFailed)
    }

    fn latch_unhealthy(&self, error: AuditError) -> AuditError {
        self.state.healthy.store(false, Ordering::Release);
        error
    }
}

impl AuditSink for JsonlAuditSink {
    fn begin(&self, start: AuditStart) -> AuditFuture<'_, AuditTransaction> {
        Box::pin(JsonlAuditSink::begin(self, start))
    }

    fn complete(
        &self,
        transaction: AuditTransaction,
        completion: AuditCompletion,
    ) -> AuditFuture<'_, ()> {
        Box::pin(JsonlAuditSink::complete(self, transaction, completion))
    }

    fn health(&self) -> AuditHealth {
        JsonlAuditSink::health(self)
    }
}
