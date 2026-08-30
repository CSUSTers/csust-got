use std::{
    collections::HashMap,
    fmt,
    sync::{Arc, Mutex, Weak},
};
use tokio::sync::{OwnedRwLockReadGuard, OwnedRwLockWriteGuard, RwLock};

#[derive(Clone, Default)]
pub struct NamespaceGate {
    locks: Arc<Mutex<HashMap<String, Weak<RwLock<()>>>>>,
}

#[derive(Debug)]
pub struct NamespaceGateError;

impl fmt::Display for NamespaceGateError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str("namespace gate state is unavailable")
    }
}

impl std::error::Error for NamespaceGateError {}

impl NamespaceGate {
    pub async fn acquire_use(
        &self,
        key: &str,
    ) -> Result<OwnedRwLockReadGuard<()>, NamespaceGateError> {
        Ok(self.lock_for(key)?.read_owned().await)
    }

    pub fn try_acquire_reset(
        &self,
        key: &str,
    ) -> Result<Option<OwnedRwLockWriteGuard<()>>, NamespaceGateError> {
        Ok(self.lock_for(key)?.try_write_owned().ok())
    }

    fn lock_for(&self, key: &str) -> Result<Arc<RwLock<()>>, NamespaceGateError> {
        let mut locks = self.locks.lock().map_err(|_| NamespaceGateError)?;
        locks.retain(|_, lock| lock.strong_count() != 0);
        if let Some(lock) = locks.get(key).and_then(Weak::upgrade) {
            return Ok(lock);
        }
        let lock = Arc::new(RwLock::new(()));
        locks.insert(key.to_string(), Arc::downgrade(&lock));
        Ok(lock)
    }
}

#[cfg(test)]
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub(crate) enum Operation {
    Read,
    Grep,
    Write,
    Edit,
    Bash,
}

#[cfg(test)]
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub(crate) enum Phase {
    LeaseAcquired,
    BashWaitReturned,
}

#[cfg(test)]
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum HookState {
    Armed,
    Reached,
    Released,
    Cancelled,
}

#[cfg(test)]
struct HookSlot {
    state: tokio::sync::watch::Sender<HookState>,
}

#[cfg(test)]
#[derive(Clone, Default)]
pub(crate) struct NamespaceUseTestHooks {
    slots: Arc<Mutex<HashMap<(Operation, Phase), Arc<HookSlot>>>>,
}

#[cfg(test)]
impl NamespaceUseTestHooks {
    pub(crate) fn arm(&self, operation: Operation, phase: Phase) -> NamespaceUseTestHook {
        let (state, receiver) = tokio::sync::watch::channel(HookState::Armed);
        let slot = Arc::new(HookSlot { state });
        let key = (operation, phase);
        let mut slots = self
            .slots
            .lock()
            .expect("namespace use hooks are available");
        assert!(
            slots.insert(key, Arc::clone(&slot)).is_none(),
            "namespace use hook is already armed for {operation:?} {phase:?}"
        );
        NamespaceUseTestHook {
            hooks: self.clone(),
            key,
            slot,
            receiver,
        }
    }

    pub(crate) async fn pause(&self, operation: Operation, phase: Phase) {
        let slot = self
            .slots
            .lock()
            .ok()
            .and_then(|slots| slots.get(&(operation, phase)).cloned());
        let Some(slot) = slot else {
            return;
        };
        slot.state.send_replace(HookState::Reached);
        let mut receiver = slot.state.subscribe();
        if matches!(
            *receiver.borrow(),
            HookState::Released | HookState::Cancelled
        ) {
            return;
        }
        let _ = receiver
            .wait_for(|state| matches!(*state, HookState::Released | HookState::Cancelled))
            .await;
    }

    fn remove(&self, key: (Operation, Phase), slot: &Arc<HookSlot>) {
        let Ok(mut slots) = self.slots.lock() else {
            return;
        };
        if slots
            .get(&key)
            .is_some_and(|current| Arc::ptr_eq(current, slot))
        {
            slots.remove(&key);
        }
    }
}

#[cfg(test)]
pub(crate) struct NamespaceUseTestHook {
    hooks: NamespaceUseTestHooks,
    key: (Operation, Phase),
    slot: Arc<HookSlot>,
    receiver: tokio::sync::watch::Receiver<HookState>,
}

#[cfg(test)]
impl NamespaceUseTestHook {
    pub(crate) async fn wait_until_reached(&mut self) {
        let state = *self
            .receiver
            .wait_for(|state| *state != HookState::Armed)
            .await
            .expect("namespace use hook sender must remain alive");
        assert_eq!(
            state,
            HookState::Reached,
            "namespace use hook was cancelled"
        );
    }

    pub(crate) fn release(&self) {
        self.slot.state.send_replace(HookState::Released);
        self.hooks.remove(self.key, &self.slot);
    }

    pub(crate) fn cancel(&self) {
        self.slot.state.send_replace(HookState::Cancelled);
        self.hooks.remove(self.key, &self.slot);
    }
}

#[cfg(test)]
impl Drop for NamespaceUseTestHook {
    fn drop(&mut self) {
        self.cancel();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn reset_try_lock_is_namespace_scoped_and_nonblocking() {
        let gate = NamespaceGate::default();
        let active = gate.acquire_use("active").await.unwrap();

        assert!(gate.try_acquire_reset("active").unwrap().is_none());
        assert!(gate.try_acquire_reset("other").unwrap().is_some());

        drop(active);
        assert!(gate.try_acquire_reset("active").unwrap().is_some());
    }

    #[tokio::test]
    async fn hooks_release_cancel_and_drop_remove_their_private_slots() {
        let hooks = NamespaceUseTestHooks::default();
        let hook = hooks.arm(Operation::Read, Phase::LeaseAcquired);
        hook.release();
        let hook = hooks.arm(Operation::Read, Phase::LeaseAcquired);
        hook.cancel();
        let hook = hooks.arm(Operation::Read, Phase::LeaseAcquired);
        drop(hook);
        let _hook = hooks.arm(Operation::Read, Phase::LeaseAcquired);
    }
}
