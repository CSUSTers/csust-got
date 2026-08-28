use super::*;
use crate::{
    fetch_protocol::{LocalRuntimeFrame, read_local_runtime_frame},
    runtime_fetch_proxy::control::{BlockingSessionFault, ReceivedControlPacket, SessionFault},
};
use std::{
    os::fd::IntoRawFd as _,
    sync::atomic::{AtomicUsize, Ordering},
};

pub(in crate::runtime_fetch_proxy::c7_test_support::lifecycle) struct BlockingSessions {
    release: Arc<(std::sync::Mutex<bool>, std::sync::Condvar)>,
    live: Arc<AtomicUsize>,
    _peers: Vec<OwnedFd>,
}

impl BlockingSessions {
    pub(in crate::runtime_fetch_proxy::c7_test_support::lifecycle) fn live(&self) -> usize {
        self.live.load(Ordering::SeqCst)
    }

    pub(in crate::runtime_fetch_proxy::c7_test_support::lifecycle) fn release(&self) {
        *self.release.0.lock().unwrap() = true;
        self.release.1.notify_all();
    }
}

pub(in crate::runtime_fetch_proxy::c7_test_support::lifecycle) async fn enqueue_pending(
    fixture: &Fixture,
) -> usize {
    let (local, stream) = socket_pair();
    let started = Arc::new(tokio::sync::Notify::new());
    send_job(
        fixture,
        stream,
        Some(SessionFault::PendingWithSignal(Arc::clone(&started))),
    )
    .await;
    started.notified().await;
    drop(local);
    fixture.permits.available_permits()
}

pub(in crate::runtime_fetch_proxy::c7_test_support::lifecycle) async fn enqueue_blocking_pair(
    fixture: &Fixture,
) -> BlockingSessions {
    let release = Arc::new((std::sync::Mutex::new(false), std::sync::Condvar::new()));
    let live = Arc::new(AtomicUsize::new(0));
    let mut peers = Vec::new();
    for _ in 0..2 {
        let (local, stream) = socket_pair();
        let started = Arc::new(tokio::sync::Notify::new());
        send_job(
            fixture,
            stream,
            Some(SessionFault::Blocking(BlockingSessionFault {
                started: Arc::clone(&started),
                live: Arc::clone(&live),
                release: Arc::clone(&release),
            })),
        )
        .await;
        started.notified().await;
        peers.push(local);
    }
    BlockingSessions {
        release,
        live,
        _peers: peers,
    }
}

pub(in crate::runtime_fetch_proxy::c7_test_support::lifecycle) async fn enqueue_signaled_fault(
    fixture: &Fixture,
    fault: SessionFault,
) {
    let (local, stream) = socket_pair();
    let started = match &fault {
        SessionFault::PanicWithSignal(started) | SessionFault::UncategorizedWithSignal(started) => {
            Arc::clone(started)
        }
        _ => panic!("fault must expose an entry signal"),
    };
    send_job(fixture, stream, Some(fault)).await;
    started.notified().await;
    drop(local);
}

pub(in crate::runtime_fetch_proxy::c7_test_support::lifecycle) async fn enqueue_valid_session(
    fixture: &Fixture,
) {
    let (local, stream) = socket_pair();
    let raw = local.into_raw_fd();
    let standard = unsafe { std::os::unix::net::UnixStream::from_raw_fd(raw) };
    standard.set_nonblocking(true).unwrap();
    let mut local = tokio::net::UnixStream::from_std(standard).unwrap();
    send_job(fixture, stream, None).await;
    assert!(matches!(
        read_local_runtime_frame(&mut local).await.unwrap(),
        LocalRuntimeFrame::Error(_)
    ));
    assert!(read_local_runtime_frame(&mut local).await.is_err());
}

async fn send_job(fixture: &Fixture, stream: OwnedFd, fault: Option<SessionFault>) {
    fixture
        .sender
        .send(SessionJob {
            packet: ReceivedControlPacket {
                metadata: packet(),
                stream,
            },
            permit: Arc::clone(&fixture.permits).try_acquire_owned().unwrap(),
            fault,
        })
        .await
        .unwrap();
}
