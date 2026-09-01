use super::*;
use crate::{
    exec::BashHealth,
    fetch_protocol::{FETCH_PROTOCOL_VERSION, FetchRequestHead},
    runtime_fetch_proxy::{
        CommandBindingPhase, CommandControlPacket, control::ReceivedControlPacket,
    },
    workspace_budget::WorkspaceBudget,
};
use std::{
    os::fd::{FromRawFd as _, OwnedFd},
    path::PathBuf,
    sync::{Arc, Mutex},
};

#[tokio::test]
async fn real_guardian_session_panic_and_error_block_success_receipt() {
    for fault in [
        super::super::control::SessionFault::Panic,
        super::super::control::SessionFault::Uncategorized,
    ] {
        let root = tempfile::tempdir().unwrap();
        let budget = WorkspaceBudget::new(root.path(), 1024).unwrap();
        let permits = Arc::new(tokio::sync::Semaphore::new(2));
        let context = Arc::new(BindingContext {
            phase: Arc::new(Mutex::new(CommandBindingPhase::Active)),
            namespace: "guardian-test".to_string(),
            namespace_key: crate::identity::namespace_storage_key("guardian-test"),
            workspace_budget: budget,
            health: BashHealth::ready(),
            issued: None,
            broker_socket: Some(PathBuf::from("unused")),
            session_permits: Arc::clone(&permits),
        });
        let (sender, receiver) = mpsc::channel(2);
        let cancel = CancellationToken::new();
        let guardian = tokio::spawn(session_guardian(receiver, context, cancel.clone()));
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
        let local = unsafe { OwnedFd::from_raw_fd(sockets[0]) };
        let stream = unsafe { OwnedFd::from_raw_fd(sockets[1]) };
        sender
            .send(SessionJob {
                packet: ReceivedControlPacket {
                    metadata: packet(),
                    stream,
                },
                permit: Arc::clone(&permits).try_acquire_owned().unwrap(),
                fault: Some(fault),
            })
            .await
            .unwrap();
        drop(local);
        tokio::task::yield_now().await;
        cancel.cancel();
        drop(sender);
        assert!(guardian.await.unwrap().is_err());
        assert_eq!(permits.available_permits(), 2);
    }
}

#[tokio::test]
async fn real_guardian_holds_two_permits_until_join_receipts_are_consumed() {
    let root = tempfile::tempdir().unwrap();
    let budget = WorkspaceBudget::new(root.path(), 1024).unwrap();
    let permits = Arc::new(tokio::sync::Semaphore::new(2));
    let context = Arc::new(BindingContext {
        phase: Arc::new(Mutex::new(CommandBindingPhase::Active)),
        namespace: "guardian-permits".to_string(),
        namespace_key: crate::identity::namespace_storage_key("guardian-permits"),
        workspace_budget: budget,
        health: BashHealth::ready(),
        issued: None,
        broker_socket: Some(PathBuf::from("unused")),
        session_permits: Arc::clone(&permits),
    });
    let (sender, receiver) = mpsc::channel(2);
    let cancel = CancellationToken::new();
    let guardian = tokio::spawn(session_guardian(receiver, context, cancel.clone()));
    let mut locals = Vec::new();
    for _ in 0..2 {
        let (local, stream) = socket_pair();
        locals.push(local);
        sender
            .send(SessionJob {
                packet: ReceivedControlPacket {
                    metadata: packet(),
                    stream,
                },
                permit: Arc::clone(&permits).try_acquire_owned().unwrap(),
                fault: Some(super::super::control::SessionFault::Pending),
            })
            .await
            .unwrap();
    }
    tokio::time::sleep(std::time::Duration::from_millis(20)).await;
    assert_eq!(permits.available_permits(), 0);
    assert!(Arc::clone(&permits).try_acquire_owned().is_err());
    cancel.cancel();
    drop(sender);
    let receipt = guardian.await.unwrap().unwrap();
    assert_eq!(receipt.spawned_sessions, 2);
    assert_eq!(receipt.joined_sessions, 2);
    assert_eq!(permits.available_permits(), 2);
    drop(locals);
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
