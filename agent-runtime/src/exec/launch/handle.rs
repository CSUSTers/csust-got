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
}

impl Drop for CommandHandle {
    fn drop(&mut self) {
        if let Some(cancel) = self.cancel.take() {
            let _ = cancel.send(true);
        }
    }
}
