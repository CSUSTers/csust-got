use super::*;
use crate::{exec::BashHealth, identity::namespace_storage_key};

#[test]
fn postrename_directory_sync_failure_is_committed_visible_and_latches_health() {
    let root = tempfile::tempdir().unwrap();
    let namespace_key = namespace_storage_key("ns");
    std::fs::create_dir(root.path().join(&namespace_key)).unwrap();
    let budget = WorkspaceBudget::new(root.path(), 1024).unwrap();
    let phase = Arc::new(Mutex::new(CommandBindingPhase::Active));
    let health = BashHealth::ready();
    let mut output = OutputCommitGuard::new_inner(
        root.path(),
        &namespace_key,
        "/workspace/result.txt",
        &budget,
        phase,
        health.clone(),
        Arc::new(|_| Err(io::Error::other("injected directory sync failure"))),
    )
    .unwrap();
    output.write_chunk(b"committed-value").unwrap();

    let outcome = output.commit_if_active().unwrap();

    assert_eq!(outcome, super::OutputCommitOutcome::Committed);
    assert_eq!(
        std::fs::read(root.path().join(namespace_key).join("result.txt")).unwrap(),
        b"committed-value"
    );
    assert!(!health.is_ready());
    assert_eq!(
        health.reason(),
        "bash unavailable: workspace durability failed"
    );
}
