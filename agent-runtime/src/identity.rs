use crate::{CommonRequest, RuntimeError};
use sha2::{Digest, Sha256};
use std::path::{Path, PathBuf};

pub(crate) const MAX_NAMESPACE_BYTES: usize = 256;
pub(crate) const MAX_RUN_ID_BYTES: usize = 128;

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct RuntimeIdentity {
    namespace: String,
    run_id: String,
    namespace_key: String,
}

impl RuntimeIdentity {
    pub(crate) fn from_common(common: &CommonRequest) -> Result<Self, RuntimeError> {
        validate_identity_value("namespace", &common.namespace, MAX_NAMESPACE_BYTES)?;
        validate_identity_value("run_id", &common.run_id, MAX_RUN_ID_BYTES)?;
        Ok(Self {
            namespace: common.namespace.clone(),
            run_id: common.run_id.clone(),
            namespace_key: namespace_storage_key(&common.namespace),
        })
    }

    pub(crate) fn namespace(&self) -> &str {
        &self.namespace
    }

    pub(crate) fn run_id(&self) -> &str {
        &self.run_id
    }

    pub(crate) fn namespace_key(&self) -> &str {
        &self.namespace_key
    }
}

pub(crate) fn namespace_storage_key(namespace: &str) -> String {
    Sha256::digest(namespace.as_bytes())
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

pub(crate) fn command_jail_root(
    root: &Path,
    identity: &RuntimeIdentity,
    command_id: &str,
) -> PathBuf {
    root.join(".runtime-jails")
        .join(identity.namespace_key())
        .join(command_id)
}

fn validate_identity_value(
    name: &str,
    value: &str,
    maximum_bytes: usize,
) -> Result<(), RuntimeError> {
    if value.trim().is_empty() {
        return Err(RuntimeError::bad_request(format!("{name} is empty")));
    }
    if value.len() > maximum_bytes {
        return Err(RuntimeError::bad_request(format!(
            "{name} exceeds {maximum_bytes} bytes"
        )));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn namespace_storage_key_is_full_lowercase_sha256() {
        let first = namespace_storage_key("a:b");
        let second = namespace_storage_key("a/b");
        assert_eq!(first.len(), 64);
        assert_eq!(second.len(), 64);
        assert!(
            first
                .bytes()
                .all(|byte| byte.is_ascii_lowercase() || byte.is_ascii_digit())
        );
        assert!(
            second
                .bytes()
                .all(|byte| byte.is_ascii_lowercase() || byte.is_ascii_digit())
        );
        assert_ne!(first, second);
    }
}
