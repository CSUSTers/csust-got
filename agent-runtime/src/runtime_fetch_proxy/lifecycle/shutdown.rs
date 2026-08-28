use super::{CommandLifecycleLease, RuntimeFetchProxy, RuntimeFetchProxyError};

impl RuntimeFetchProxy {
    pub async fn shutdown(&self) -> Result<(), RuntimeFetchProxyError> {
        let entries = self
            .inner
            .registry
            .lock()
            .map_err(|_| RuntimeFetchProxyError::new("fetch proxy registry is poisoned"))?
            .values()
            .cloned()
            .collect::<Vec<_>>();
        let mut failures = 0_usize;
        for entry in entries {
            if CommandLifecycleLease::from_entry(Some(entry))
                .revoke_and_wait()
                .await
                .is_err()
            {
                failures += 1;
            }
        }
        if failures > 0 {
            return Err(RuntimeFetchProxyError::new(format!(
                "fetch proxy shutdown failed for {failures} command binding(s)"
            )));
        }
        Ok(())
    }
}
