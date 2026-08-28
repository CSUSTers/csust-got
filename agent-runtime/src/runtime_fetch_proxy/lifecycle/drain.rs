use super::{BindingDrainError, ControlReaderOutcome, GuardianTaskOutcome};
use crate::runtime_fetch_proxy::{OWNER_DRAIN_TIMEOUT, registry::BindingEntry};
use tokio::time::timeout;

pub(super) async fn await_control_reader<F>(
    entry: &BindingEntry,
    slow_observer: &mut Option<F>,
) -> Result<ControlReaderOutcome, BindingDrainError>
where
    F: FnOnce(),
{
    if let Some(outcome) = *entry.control_outcome.lock().await {
        return Ok(outcome);
    }
    let mut slot = entry.control_reader.lock().await;
    let Some(task) = slot.as_mut() else {
        return Err(BindingDrainError::ControlReceiptMissing);
    };
    let joined = match timeout(OWNER_DRAIN_TIMEOUT, &mut *task).await {
        Ok(joined) => joined,
        Err(_) => {
            notify_slow(slow_observer);
            return Err(BindingDrainError::DrainPending);
        }
    };
    slot.take();
    let outcome = match joined {
        Ok(Ok(_)) => ControlReaderOutcome::Completed,
        Ok(Err(_)) => ControlReaderOutcome::Error,
        Err(error) if error.is_cancelled() => ControlReaderOutcome::Cancelled,
        Err(_) => ControlReaderOutcome::Panicked,
    };
    *entry.control_outcome.lock().await = Some(outcome);
    Ok(outcome)
}

pub(super) async fn await_guardian<F>(
    entry: &BindingEntry,
    slow_observer: &mut Option<F>,
) -> Result<GuardianTaskOutcome, BindingDrainError>
where
    F: FnOnce(),
{
    if let Some(outcome) = *entry.guardian_outcome.lock().await {
        return Ok(outcome);
    }
    let mut slot = entry.guardian.lock().await;
    let Some(task) = slot.as_mut() else {
        return Err(BindingDrainError::GuardianReceiptMissing);
    };
    let joined = match timeout(OWNER_DRAIN_TIMEOUT, &mut *task).await {
        Ok(joined) => joined,
        Err(_) => {
            notify_slow(slow_observer);
            return Err(BindingDrainError::DrainPending);
        }
    };
    slot.take();
    let outcome = match joined {
        Ok(Ok(receipt)) => GuardianTaskOutcome::Success(receipt),
        Ok(Err(_)) => GuardianTaskOutcome::Error,
        Err(error) if error.is_cancelled() => GuardianTaskOutcome::Cancelled,
        Err(_) => GuardianTaskOutcome::Panicked,
    };
    *entry.guardian_outcome.lock().await = Some(outcome);
    Ok(outcome)
}

fn notify_slow<F>(observer: &mut Option<F>)
where
    F: FnOnce(),
{
    if let Some(observer) = observer.take() {
        observer();
    }
}
