use super::super::supervisor::CommandHandle;
use super::super::*;

impl CommandHandle {
    pub async fn wait(mut self) -> Result<CommandOutput, SupervisorError> {
        let result = self
            .result
            .take()
            .ok_or_else(|| SupervisorError::Command("command was already awaited".to_string()))?;
        result.await.map_err(|error| {
            SupervisorError::Command(format!("command supervisor failed: {error}"))
        })?
    }

    pub async fn cancel(mut self) -> Result<CommandOutput, SupervisorError> {
        if let Some(cancel) = self.cancel.take() {
            let _ = cancel.send(true);
        }
        self.wait().await
    }

    pub async fn wait_or_caller_drop(
        mut self,
        mut caller_drop: tokio::sync::watch::Receiver<bool>,
    ) -> Result<CommandOutput, SupervisorError> {
        let mut result = self
            .result
            .take()
            .ok_or_else(|| SupervisorError::Command("command was already awaited".to_string()))?;
        let completed = loop {
            if *caller_drop.borrow() {
                break None;
            }
            tokio::select! {
                completed = &mut result => break Some(completed),
                changed = caller_drop.changed() => {
                    if changed.is_err() || *caller_drop.borrow() {
                        break None;
                    }
                }
            }
        };
        let output = match completed {
            Some(output) => output,
            None => {
                if let Some(cancel) = self.cancel.take() {
                    let _ = cancel.send(true);
                }
                result.await
            }
        };
        output.map_err(|error| {
            SupervisorError::Command(format!("command supervisor failed: {error}"))
        })?
    }
}

impl Drop for CommandHandle {
    fn drop(&mut self) {
        if let Some(cancel) = self.cancel.take() {
            let _ = cancel.send(true);
        }
    }
}
