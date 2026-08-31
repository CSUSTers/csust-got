use super::ConfigError;
use std::{path::PathBuf, time::Duration};

pub(crate) fn required_string(
    get: &impl Fn(&str) -> Option<String>,
    name: &str,
) -> Result<String, ConfigError> {
    get(name)
        .filter(|value| !value.trim().is_empty())
        .map(|value| value.trim().to_string())
        .ok_or_else(|| ConfigError::new(format!("{name} is required and must not be blank")))
}

pub(crate) fn required_absolute_path(
    get: &impl Fn(&str) -> Option<String>,
    name: &str,
) -> Result<PathBuf, ConfigError> {
    let path = PathBuf::from(required_string(get, name)?);
    if !path.is_absolute() && !path.has_root() {
        return Err(ConfigError::new(format!("{name} must be an absolute path")));
    }
    Ok(path)
}

pub(crate) fn required_list(
    get: &impl Fn(&str) -> Option<String>,
    name: &str,
) -> Result<Vec<String>, ConfigError> {
    let raw = required_string(get, name)?;
    let values = raw
        .split(',')
        .map(str::trim)
        .map(str::to_string)
        .collect::<Vec<_>>();
    if values.is_empty() || values.iter().any(String::is_empty) {
        return Err(ConfigError::new(format!(
            "{name} must be a non-empty comma-separated list"
        )));
    }
    Ok(values)
}

pub(crate) fn required_positive_number<T>(
    get: &impl Fn(&str) -> Option<String>,
    name: &str,
) -> Result<T, ConfigError>
where
    T: std::str::FromStr + PartialEq + From<u8>,
{
    let value = required_string(get, name)?
        .parse::<T>()
        .map_err(|_| ConfigError::new(format!("{name} must be a positive integer")))?;
    if value == T::from(0) {
        return Err(ConfigError::new(format!(
            "{name} must be greater than zero"
        )));
    }
    Ok(value)
}

pub(crate) fn bounded_number<T>(
    get: &impl Fn(&str) -> Option<String>,
    name: &str,
    default: T,
    maximum: T,
) -> Result<T, ConfigError>
where
    T: std::str::FromStr + PartialEq + PartialOrd + From<u8> + Copy,
{
    let Some(raw) = get(name) else {
        return Ok(default);
    };
    let value = raw
        .trim()
        .parse::<T>()
        .map_err(|_| ConfigError::new(format!("{name} must be a positive integer")))?;
    if raw.trim().is_empty() || value == T::from(0) || value > maximum {
        return Err(ConfigError::new(format!(
            "{name} is outside the approved range"
        )));
    }
    Ok(value)
}

pub(crate) fn bounded_nonnegative_number<T>(
    get: &impl Fn(&str) -> Option<String>,
    name: &str,
    default: T,
    maximum: T,
) -> Result<T, ConfigError>
where
    T: std::str::FromStr + PartialOrd + Copy,
{
    let Some(raw) = get(name) else {
        return Ok(default);
    };
    let trimmed = raw.trim();
    let value = trimmed
        .parse::<T>()
        .map_err(|_| ConfigError::new(format!("{name} must be a non-negative integer")))?;
    if trimmed.is_empty() || value > maximum {
        return Err(ConfigError::new(format!(
            "{name} is outside the approved range"
        )));
    }
    Ok(value)
}

pub(crate) fn bounded_duration(
    get: &impl Fn(&str) -> Option<String>,
    name: &str,
    maximum_ms: u64,
) -> Result<Duration, ConfigError> {
    Ok(Duration::from_millis(bounded_number(
        get, name, maximum_ms, maximum_ms,
    )?))
}

pub(crate) fn load_signing_key(path: &PathBuf, owner: &str) -> Result<Vec<u8>, ConfigError> {
    let key = std::fs::read(path).map_err(|_| {
        ConfigError::new(format!(
            "AGENT_FETCH_HMAC_KEY_FILE cannot be read at {owner} startup"
        ))
    })?;
    if key.is_empty() {
        return Err(ConfigError::new(
            "AGENT_FETCH_HMAC_KEY_FILE must not contain an empty key",
        ));
    }
    Ok(key)
}

pub(super) fn optional_string(
    get: &impl Fn(&str) -> Option<String>,
    name: &str,
    default: &str,
) -> Result<String, ConfigError> {
    match get(name) {
        Some(value) if value.trim().is_empty() => {
            Err(ConfigError::new(format!("{name} must not be blank")))
        }
        Some(value) => Ok(value.trim().to_string()),
        None => Ok(default.to_string()),
    }
}

pub(super) fn optional_path(
    get: &impl Fn(&str) -> Option<String>,
    name: &str,
    default: &str,
) -> Result<PathBuf, ConfigError> {
    Ok(PathBuf::from(optional_string(get, name, default)?))
}

pub(super) fn optional_disableable_path(
    get: &impl Fn(&str) -> Option<String>,
    name: &str,
    default: &str,
) -> Result<Option<PathBuf>, ConfigError> {
    match get(name) {
        Some(value) if value.trim().is_empty() => Ok(None),
        Some(value) => Ok(Some(PathBuf::from(value.trim()))),
        None => Ok(Some(PathBuf::from(default))),
    }
}

pub(super) fn optional_supplied_path(
    get: &impl Fn(&str) -> Option<String>,
    name: &str,
) -> Result<Option<PathBuf>, ConfigError> {
    match get(name) {
        Some(value) if value.trim().is_empty() => {
            Err(ConfigError::new(format!("{name} must not be blank")))
        }
        Some(value) => Ok(Some(PathBuf::from(value.trim()))),
        None => Ok(None),
    }
}

pub(super) fn optional_bool(
    get: &impl Fn(&str) -> Option<String>,
    name: &str,
    default: bool,
) -> Result<bool, ConfigError> {
    match get(name).as_deref() {
        None => Ok(default),
        Some("true") => Ok(true),
        Some("false") => Ok(false),
        Some(_) => Err(ConfigError::new(format!(
            "{name} must be exactly true or false"
        ))),
    }
}
