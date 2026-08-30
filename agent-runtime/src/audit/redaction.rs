use sha2::{Digest, Sha256};

pub(super) fn sha256_hex(value: &[u8]) -> String {
    let mut hasher = Sha256::new();
    hasher.update(value);
    hasher
        .finalize()
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

pub(super) fn redacted_origin(value: &str) -> String {
    let value = value.trim();
    let Some((scheme, remainder)) = value.split_once("://") else {
        return "invalid-origin".to_string();
    };
    if !matches!(scheme, "http" | "https") {
        return "invalid-origin".to_string();
    }
    let authority = remainder
        .split(['/', '?', '#'])
        .next()
        .unwrap_or_default()
        .rsplit('@')
        .next()
        .unwrap_or_default();
    if authority.is_empty() {
        return "invalid-origin".to_string();
    }
    format!("{scheme}://{authority}")
}

pub(super) fn redacted_header_name(value: &str) -> String {
    let value = value.trim();
    if value
        .bytes()
        .all(|byte| byte.is_ascii_alphanumeric() || byte == b'-')
        && !value.is_empty()
    {
        value.to_ascii_lowercase()
    } else {
        "invalid-header-name".to_string()
    }
}
