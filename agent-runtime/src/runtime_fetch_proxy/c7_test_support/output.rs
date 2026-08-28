use super::super::{
    CommandBindingPhase, OutputCommitGuard, OutputCommitOutcome, RuntimeFetchProxyError,
    output::OutputFault, terminal::LocalTerminalWriter,
};
use crate::{
    exec::BashHealth,
    fetch_protocol::{ErrorCode, LocalRuntimeFrame, read_local_runtime_frame},
    workspace_budget::WorkspaceBudget,
};
use std::{
    path::{Path, PathBuf},
    sync::{
        Arc, Mutex,
        atomic::{AtomicU64, Ordering},
    },
};

static SEQUENCE: AtomicU64 = AtomicU64::new(0);

pub struct OutputTerminalReceipt {
    pub codes: Vec<ErrorCode>,
    pub exactly_once: bool,
}

pub struct PreRenameReceipt {
    pub code: ErrorCode,
    pub old_preserved: bool,
    pub temporary_absent: bool,
}

pub struct PostRenameReceipt {
    pub committed: bool,
    pub new_visible: bool,
    pub health_ready: bool,
    pub health_reason: String,
}

pub async fn policy_terminal_receipt() -> OutputTerminalReceipt {
    let root = FixtureRoot::new("policy");
    let budget = WorkspaceBudget::new(root.path(), 4).unwrap();
    let phase = || Arc::new(Mutex::new(CommandBindingPhase::Active));
    let path_error = take_error(OutputCommitGuard::new(
        root.path(),
        "ns",
        "/workspace/../escape",
        &budget,
        phase(),
    ));
    let mut capacity =
        OutputCommitGuard::new(root.path(), "ns", "/workspace/capacity", &budget, phase()).unwrap();
    let capacity_error = capacity.write_chunk(b"too-large").unwrap_err();
    drop(capacity);
    let busy_guard =
        OutputCommitGuard::new(root.path(), "ns", "/workspace/busy", &budget, phase()).unwrap();
    let busy_error = take_error(OutputCommitGuard::new(
        root.path(),
        "ns",
        "/workspace/busy",
        &budget,
        phase(),
    ));
    drop(busy_guard);
    terminal_receipt([path_error, capacity_error, busy_error]).await
}

pub async fn internal_terminal_receipt() -> OutputTerminalReceipt {
    let read_only = Path::new("/proc");
    let read_only_budget = WorkspaceBudget::new(read_only, 1024).unwrap();
    let open_error = take_error(OutputCommitGuard::new(
        read_only,
        "agent-runtime-c7-read-only",
        "/workspace/open",
        &read_only_budget,
        active_phase(),
    ));
    let root = FixtureRoot::new("internal");
    let budget = WorkspaceBudget::new(root.path(), 1024).unwrap();
    let write_error = fault_error(root.path(), &budget, "write", OutputFault::Write, false);
    let sync_error = fault_error(root.path(), &budget, "sync", OutputFault::FileSync, true);
    let rename_error = fault_error(root.path(), &budget, "rename", OutputFault::Rename, true);
    terminal_receipt([open_error, write_error, sync_error, rename_error]).await
}

pub async fn pre_rename_receipt() -> PreRenameReceipt {
    let root = FixtureRoot::new("pre-rename");
    let namespace = root.path().join("ns");
    std::fs::create_dir(&namespace).unwrap();
    let destination = namespace.join("result");
    std::fs::write(&destination, b"old").unwrap();
    let budget = WorkspaceBudget::new(root.path(), 1024).unwrap();
    let mut guard = OutputCommitGuard::new(
        root.path(),
        "ns",
        "/workspace/result",
        &budget,
        active_phase(),
    )
    .unwrap();
    guard.set_fault(OutputFault::Rename);
    guard.write_chunk(b"new").unwrap();
    let error = guard.commit_if_active().unwrap_err();
    let terminal = terminal_receipt([error]).await;
    PreRenameReceipt {
        code: terminal.codes[0],
        old_preserved: std::fs::read(&destination).unwrap() == b"old",
        temporary_absent: no_temporary(&namespace),
    }
}

pub fn post_rename_receipt() -> PostRenameReceipt {
    let root = FixtureRoot::new("post-rename");
    let budget = WorkspaceBudget::new(root.path(), 1024).unwrap();
    let health = BashHealth::ready();
    let mut guard = OutputCommitGuard::with_directory_sync_failure(
        root.path(),
        "ns",
        "/workspace/result",
        &budget,
        active_phase(),
        health.clone(),
    )
    .unwrap();
    guard.write_chunk(b"new").unwrap();
    let committed = guard.commit_if_active().unwrap() == OutputCommitOutcome::Committed;
    PostRenameReceipt {
        committed,
        new_visible: std::fs::read(root.path().join("ns/result")).unwrap() == b"new",
        health_ready: health.is_ready(),
        health_reason: health.reason(),
    }
}

fn fault_error(
    root: &Path,
    budget: &WorkspaceBudget,
    name: &str,
    fault: OutputFault,
    write: bool,
) -> RuntimeFetchProxyError {
    let path = format!("/workspace/{name}");
    let mut guard = OutputCommitGuard::new(root, "ns", &path, budget, active_phase()).unwrap();
    guard.set_fault(fault);
    if write {
        guard.write_chunk(b"bytes").unwrap();
        guard.commit_if_active().unwrap_err()
    } else {
        guard.write_chunk(b"bytes").unwrap_err()
    }
}

fn take_error(result: Result<OutputCommitGuard, RuntimeFetchProxyError>) -> RuntimeFetchProxyError {
    match result {
        Ok(_) => panic!("expected output setup failure"),
        Err(error) => error,
    }
}

async fn terminal_receipt<const N: usize>(
    errors: [RuntimeFetchProxyError; N],
) -> OutputTerminalReceipt {
    let mut codes = Vec::with_capacity(N);
    let mut exactly_once = true;
    for error in errors {
        let (reader, writer) = tokio::net::UnixStream::pair().unwrap();
        let mut terminal = LocalTerminalWriter::new(writer);
        terminal.send_proxy_error(&error).await;
        terminal.send_proxy_error(&error).await;
        drop(terminal);
        let mut reader = reader;
        let frame = read_local_runtime_frame(&mut reader).await.unwrap();
        let LocalRuntimeFrame::Error(error) = frame else {
            panic!("expected terminal error");
        };
        codes.push(error.code);
        exactly_once &= read_local_runtime_frame(&mut reader).await.is_err();
    }
    OutputTerminalReceipt {
        codes,
        exactly_once,
    }
}

fn active_phase() -> Arc<Mutex<CommandBindingPhase>> {
    Arc::new(Mutex::new(CommandBindingPhase::Active))
}

fn no_temporary(directory: &Path) -> bool {
    std::fs::read_dir(directory).unwrap().all(|entry| {
        !entry
            .unwrap()
            .file_name()
            .to_string_lossy()
            .contains(".tmp")
    })
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
