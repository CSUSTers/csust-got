mod jobs;

pub(super) use jobs::{
    enqueue_blocking_pair, enqueue_pending, enqueue_signaled_fault, enqueue_valid_session,
};

use super::super::super::{
    CommandBindingPhase, CommandControlPacket, CommandLifecycleLease, RuntimeFetchProxy,
    RuntimeFetchProxyError,
    control::SessionJob,
    guardian::session_guardian,
    registry::{BindingContext, BindingEntry, ControlReport},
};
use crate::{
    exec::BashHealth,
    fetch_protocol::{FETCH_PROTOCOL_VERSION, FetchRequestHead},
    workspace_budget::WorkspaceBudget,
};
use std::{
    os::fd::{FromRawFd as _, OwnedFd},
    path::{Path, PathBuf},
    sync::{
        Arc, Mutex,
        atomic::{AtomicU64, Ordering},
    },
};
use tokio::sync::{Mutex as AsyncMutex, mpsc, oneshot};
use tokio_util::sync::CancellationToken;

static SEQUENCE: AtomicU64 = AtomicU64::new(0);

pub(super) enum GuardianMode {
    Exact,
    Mismatch,
    Hold(oneshot::Receiver<()>),
}

pub(super) struct Fixture {
    _root: FixtureRoot,
    pub(super) proxy: RuntimeFetchProxy,
    pub(super) lifecycle: CommandLifecycleLease,
    pub(super) entry: Arc<BindingEntry>,
    sender: mpsc::Sender<SessionJob>,
    permits: Arc<tokio::sync::Semaphore>,
}

pub(super) fn binding_fixture<C>(health: BashHealth, control: C, mode: GuardianMode) -> Fixture
where
    C: std::future::Future<Output = Result<ControlReport, RuntimeFetchProxyError>> + Send + 'static,
{
    let root = FixtureRoot::new("binding");
    let budget = WorkspaceBudget::new(root.path(), 1024).unwrap();
    let proxy = RuntimeFetchProxy::disabled(budget.clone());
    let context = Arc::new(BindingContext {
        phase: Arc::new(Mutex::new(CommandBindingPhase::Active)),
        namespace: "c7".to_string(),
        workspace_budget: budget,
        health,
        issued: None,
        broker_socket: None,
        session_permits: Arc::new(tokio::sync::Semaphore::new(2)),
    });
    let permits = Arc::clone(&context.session_permits);
    let (sender, receiver) = mpsc::channel(2);
    let guardian_cancel = CancellationToken::new();
    let guardian_context = Arc::clone(&context);
    let guardian_cancel_task = guardian_cancel.clone();
    let guardian = tokio::spawn(async move {
        let mut receipt =
            session_guardian(receiver, guardian_context, guardian_cancel_task).await?;
        match mode {
            GuardianMode::Exact => {}
            GuardianMode::Mismatch => {
                receipt.joined_sessions = receipt.joined_sessions.saturating_sub(1)
            }
            GuardianMode::Hold(release) => {
                let _ = release.await;
            }
        }
        Ok(receipt)
    });
    let control_reader = tokio::spawn(control);
    let entry = Arc::new(BindingEntry {
        command_id: "c7-binding".to_string(),
        context,
        control_cancel: CancellationToken::new(),
        guardian_cancel,
        control_reader: AsyncMutex::new(Some(control_reader)),
        control_outcome: AsyncMutex::new(None),
        guardian: AsyncMutex::new(Some(guardian)),
        guardian_outcome: AsyncMutex::new(None),
        job_sender: Mutex::new(Some(sender.clone())),
        proxy: Arc::downgrade(&proxy.inner),
    });
    proxy
        .inner
        .registry
        .lock()
        .unwrap()
        .insert(entry.command_id.clone(), Arc::clone(&entry));
    let lifecycle = CommandLifecycleLease::from_entry(Some(Arc::clone(&entry)));
    Fixture {
        _root: root,
        proxy,
        lifecycle,
        entry,
        sender,
        permits,
    }
}

impl Fixture {
    pub(super) fn available_permits(&self) -> usize {
        self.permits.available_permits()
    }
}

pub(super) fn workspace_budget(label: &str) -> WorkspaceBudget {
    let path = fixture_path(label);
    std::fs::create_dir(&path).unwrap();
    WorkspaceBudget::new(path, 1024).unwrap()
}

fn packet() -> CommandControlPacket {
    CommandControlPacket {
        protocol_version: FETCH_PROTOCOL_VERSION,
        request: FetchRequestHead {
            protocol_version: FETCH_PROTOCOL_VERSION,
            method: "GET".to_string(),
            url: "https://example.com/".to_string(),
            headers: Vec::new(),
            follow: false,
            check_status: false,
            timeout_ms: None,
            declared_body_bytes: Some(0),
        },
        output_path: None,
    }
}

fn socket_pair() -> (OwnedFd, OwnedFd) {
    let mut sockets = [-1_i32; 2];
    assert_eq!(
        unsafe {
            libc::socketpair(
                libc::AF_UNIX,
                libc::SOCK_STREAM | libc::SOCK_CLOEXEC,
                0,
                sockets.as_mut_ptr(),
            )
        },
        0
    );
    unsafe {
        (
            OwnedFd::from_raw_fd(sockets[0]),
            OwnedFd::from_raw_fd(sockets[1]),
        )
    }
}

fn fixture_path(label: &str) -> PathBuf {
    let sequence = SEQUENCE.fetch_add(1, Ordering::Relaxed);
    PathBuf::from(format!(
        "/tmp/agent-runtime-c7-{}-{sequence}-{label}",
        std::process::id()
    ))
}

struct FixtureRoot(PathBuf);
impl FixtureRoot {
    fn new(label: &str) -> Self {
        let path = fixture_path(label);
        let _ = std::fs::remove_dir_all(&path);
        std::fs::create_dir(&path).unwrap();
        Self(path)
    }
    fn path(&self) -> &Path {
        &self.0
    }
}
impl Drop for FixtureRoot {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}
