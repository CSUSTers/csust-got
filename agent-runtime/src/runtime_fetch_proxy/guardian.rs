use super::{
    MAX_ACTIVE_LOCAL_SESSIONS, RuntimeFetchProxyError,
    control::SessionJob,
    registry::{BindingContext, GuardianReport},
    session::serve_local_session,
    terminal::SessionTaskResult,
};
use std::{collections::HashMap, sync::Arc};
use tokio::{
    sync::{OwnedSemaphorePermit, mpsc},
    task::{Id, JoinSet},
};
use tokio_util::sync::CancellationToken;

pub(super) async fn session_guardian(
    mut jobs: mpsc::Receiver<SessionJob>,
    context: Arc<BindingContext>,
    cancel: CancellationToken,
) -> Result<GuardianReport, RuntimeFetchProxyError> {
    let mut sessions = JoinSet::<SessionTaskResult>::new();
    let mut permits = HashMap::<Id, OwnedSemaphorePermit>::new();
    let mut spawned_sessions = 0_usize;
    let mut joined_sessions = 0_usize;
    let mut session_failure = false;
    loop {
        tokio::select! {
            biased;
            _ = cancel.cancelled() => break,
            joined = sessions.join_next_with_id(), if !sessions.is_empty() => {
                observe_join(
                    joined,
                    &mut permits,
                    &mut joined_sessions,
                    &mut session_failure,
                );
            }
            job = jobs.recv(), if sessions.len() < MAX_ACTIVE_LOCAL_SESSIONS => {
                let Some(job) = job else {
                    if sessions.is_empty() {
                        break;
                    }
                    continue;
                };
                let SessionJob {
                    packet,
                    permit,
                    #[cfg(any(test, feature = "c7-test-support"))]
                    fault,
                } = job;
                let session_context = Arc::clone(&context);
                let session_cancel = cancel.child_token();
                let task = sessions.spawn(async move {
                    #[cfg(any(test, feature = "c7-test-support"))]
                    match fault {
                        #[cfg(test)]
                        Some(super::control::SessionFault::Panic) => {
                            panic!("injected session panic")
                        }
                        #[cfg(test)]
                        Some(super::control::SessionFault::Uncategorized) => {
                            return Err(RuntimeFetchProxyError::new(
                                "injected uncategorized session failure",
                            ));
                        }
                        #[cfg(test)]
                        Some(super::control::SessionFault::Pending) => {
                            std::future::pending::<()>().await;
                        }
                        #[cfg(feature = "c7-test-support")]
                        Some(super::control::SessionFault::PendingWithSignal(started)) => {
                            started.notify_one();
                            std::future::pending::<()>().await;
                        }
                        #[cfg(feature = "c7-test-support")]
                        Some(super::control::SessionFault::Blocking(blocking)) => {
                            blocking.wait()?;
                        }
                        #[cfg(feature = "c7-test-support")]
                        Some(super::control::SessionFault::PanicWithSignal(started)) => {
                            started.notify_one();
                            panic!("c7 session panic");
                        }
                        #[cfg(feature = "c7-test-support")]
                        Some(super::control::SessionFault::UncategorizedWithSignal(started)) => {
                            started.notify_one();
                            return Err(RuntimeFetchProxyError::new(
                                "c7 uncategorized session failure",
                            ));
                        }
                        None => {}
                    }
                    serve_local_session(packet, session_context, session_cancel).await
                });
                if permits.insert(task.id(), permit).is_some() {
                    session_failure = true;
                }
                spawned_sessions += 1;
            }
        }
    }
    jobs.close();
    while jobs.try_recv().is_ok() {}
    sessions.abort_all();
    while !sessions.is_empty() {
        let joined = sessions.join_next_with_id().await;
        observe_join(
            joined,
            &mut permits,
            &mut joined_sessions,
            &mut session_failure,
        );
    }
    let receipt = GuardianReport {
        spawned_sessions,
        joined_sessions,
        joinset_empty: sessions.is_empty(),
        job_channel_closed: jobs.is_closed(),
    };
    if session_failure || !permits.is_empty() {
        return Err(RuntimeFetchProxyError::new(
            "session guardian observed an invalid session receipt",
        ));
    }
    Ok(receipt)
}

#[cfg(test)]
mod tests;

fn observe_join(
    joined: Option<Result<(Id, SessionTaskResult), tokio::task::JoinError>>,
    permits: &mut HashMap<Id, OwnedSemaphorePermit>,
    joined_sessions: &mut usize,
    session_failure: &mut bool,
) {
    let Some(joined) = joined else {
        *session_failure = true;
        return;
    };
    *joined_sessions += 1;
    let (id, valid) = match joined {
        Ok((id, Ok(receipt))) => {
            let terminal_observed = matches!(
                receipt.terminal,
                super::terminal::TerminalDelivery::Delivered
                    | super::terminal::TerminalDelivery::Unavailable
            );
            (id, terminal_observed)
        }
        Ok((id, Err(_))) => (id, false),
        Err(error) if error.is_cancelled() => (error.id(), true),
        Err(error) => (error.id(), false),
    };
    if permits.remove(&id).is_none() || !valid {
        *session_failure = true;
    }
}
