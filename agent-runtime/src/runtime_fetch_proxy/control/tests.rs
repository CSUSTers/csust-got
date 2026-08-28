use super::*;
use crate::runtime_fetch_proxy::CommandBindingPhase;
use std::sync::{
    Barrier, Mutex,
    atomic::{AtomicUsize, Ordering},
    mpsc,
};

#[test]
fn session_admission_and_revoke_are_linearized_in_both_barrier_orders() {
    let phase = Arc::new(Mutex::new(CommandBindingPhase::Active));
    let connects = Arc::new(AtomicUsize::new(0));
    let (admitted_tx, admitted_rx) = mpsc::sync_channel(0);
    let (release_tx, release_rx) = mpsc::sync_channel(0);
    let packet_phase = Arc::clone(&phase);
    let packet_connects = Arc::clone(&connects);
    let packet_wins = std::thread::spawn(move || {
        with_active_binding(&packet_phase, || {
            packet_connects.fetch_add(1, Ordering::SeqCst);
            admitted_tx.send(()).unwrap();
            release_rx.recv().unwrap();
        })
        .unwrap()
    });
    admitted_rx.recv().unwrap();
    let (revoked_tx, revoked_rx) = mpsc::sync_channel(0);
    let revoke_phase = Arc::clone(&phase);
    let revoke = std::thread::spawn(move || {
        *revoke_phase.lock().unwrap() = CommandBindingPhase::Revoked;
        revoked_tx.send(()).unwrap();
    });
    assert!(matches!(
        revoked_rx.try_recv(),
        Err(mpsc::TryRecvError::Empty)
    ));
    release_tx.send(()).unwrap();
    assert_eq!(packet_wins.join().unwrap(), Some(()));
    revoked_rx.recv().unwrap();
    revoke.join().unwrap();
    assert_eq!(connects.load(Ordering::SeqCst), 1);

    let phase = Arc::new(Mutex::new(CommandBindingPhase::Active));
    let connects = Arc::new(AtomicUsize::new(0));
    let gate = Arc::new(Barrier::new(2));
    let mut revoke_guard = phase.lock().unwrap();
    let packet_phase = Arc::clone(&phase);
    let packet_connects = Arc::clone(&connects);
    let packet_gate = Arc::clone(&gate);
    let revoke_wins = std::thread::spawn(move || {
        packet_gate.wait();
        with_active_binding(&packet_phase, || {
            packet_connects.fetch_add(1, Ordering::SeqCst);
        })
        .unwrap()
    });
    gate.wait();
    *revoke_guard = CommandBindingPhase::Revoked;
    drop(revoke_guard);
    assert_eq!(revoke_wins.join().unwrap(), None);
    assert_eq!(connects.load(Ordering::SeqCst), 0);
}
