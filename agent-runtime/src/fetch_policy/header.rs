use super::{PolicyCode, PolicyConfig, PolicyError, policy_error};
use http::{HeaderMap, HeaderName, HeaderValue};
use std::collections::HashSet;

#[derive(Clone, Debug)]
pub struct ReviewedHeaders {
    pub headers: HeaderMap,
    pub wire_bytes: u64,
    sensitive: HashSet<HeaderName>,
}

impl ReviewedHeaders {
    pub fn contains(&self, name: &str) -> bool {
        HeaderName::from_bytes(name.as_bytes())
            .ok()
            .is_some_and(|name| self.headers.contains_key(name))
    }

    pub fn is_sensitive(&self, name: &str) -> bool {
        HeaderName::from_bytes(name.as_bytes())
            .ok()
            .is_some_and(|name| self.sensitive.contains(&name))
    }

    pub(crate) fn strip_sensitive_with(
        mut self,
        configured: &[String],
    ) -> Result<Self, PolicyError> {
        for raw in configured {
            self.sensitive.insert(parse_name(raw)?);
        }
        for name in &self.sensitive {
            self.headers.remove(name);
        }
        self.wire_bytes = map_wire_bytes(&self.headers)?;
        Ok(self)
    }
}

#[derive(Clone, Debug)]
pub struct HeaderPolicy {
    config: PolicyConfig,
}

impl HeaderPolicy {
    pub fn new(config: PolicyConfig) -> Self {
        Self { config }
    }

    pub fn review(&self, raw: &[(String, String)]) -> Result<ReviewedHeaders, PolicyError> {
        let sensitive = sensitive_names(&self.config)?;
        let mut headers = HeaderMap::new();
        let mut wire_bytes = 0_u64;
        for (raw_name, raw_value) in raw {
            let name = parse_name(raw_name)?;
            if is_forbidden(&name) {
                return Err(policy_error(
                    PolicyCode::ForbiddenHeader,
                    "transport and proxy headers are not allowed",
                ));
            }
            let value = parse_value(raw_value)?;
            wire_bytes = checked_wire_add(wire_bytes, &name, &value)?;
            if wire_bytes > self.config.request_header_bytes {
                return Err(policy_error(
                    PolicyCode::BudgetExceeded,
                    "request headers exceed the byte limit",
                ));
            }
            headers.append(name, value);
        }
        Ok(ReviewedHeaders {
            headers,
            wire_bytes,
            sensitive,
        })
    }
}

impl Default for HeaderPolicy {
    fn default() -> Self {
        Self::new(PolicyConfig::approved_defaults())
    }
}

fn parse_name(raw: &str) -> Result<HeaderName, PolicyError> {
    HeaderName::from_bytes(raw.as_bytes())
        .map_err(|_| policy_error(PolicyCode::InvalidHeader, "header name is not an RFC token"))
}

fn parse_value(raw: &str) -> Result<HeaderValue, PolicyError> {
    if raw
        .as_bytes()
        .iter()
        .any(|byte| *byte != b'\t' && (*byte < 0x20 || *byte == 0x7f))
    {
        return Err(policy_error(
            PolicyCode::InvalidHeader,
            "header value contains forbidden control bytes",
        ));
    }
    HeaderValue::from_bytes(raw.as_bytes()).map_err(|_| {
        policy_error(
            PolicyCode::InvalidHeader,
            "header value is not valid HTTP field content",
        )
    })
}

fn sensitive_names(config: &PolicyConfig) -> Result<HashSet<HeaderName>, PolicyError> {
    let mut sensitive = ["authorization", "cookie", "x-api-key"]
        .into_iter()
        .map(|name| HeaderName::from_static(name))
        .collect::<HashSet<_>>();
    for raw in &config.credential_header_names {
        sensitive.insert(parse_name(raw)?);
    }
    Ok(sensitive)
}

fn is_forbidden(name: &HeaderName) -> bool {
    let name = name.as_str();
    matches!(
        name,
        "host"
            | "content-length"
            | "transfer-encoding"
            | "connection"
            | "upgrade"
            | "te"
            | "trailer"
            | "proxy-authorization"
            | "proxy-authenticate"
            | "proxy-connection"
            | "keep-alive"
            | "forwarded"
            | "via"
            | "x-real-ip"
    ) || name.starts_with("x-forwarded-")
}

fn checked_wire_add(
    current: u64,
    name: &HeaderName,
    value: &HeaderValue,
) -> Result<u64, PolicyError> {
    let name_bytes = u64::try_from(name.as_str().len()).map_err(|_| overflow())?;
    let value_bytes = u64::try_from(value.as_bytes().len()).map_err(|_| overflow())?;
    current
        .checked_add(name_bytes)
        .and_then(|total| total.checked_add(2))
        .and_then(|total| total.checked_add(value_bytes))
        .and_then(|total| total.checked_add(2))
        .ok_or_else(overflow)
}

fn map_wire_bytes(headers: &HeaderMap) -> Result<u64, PolicyError> {
    headers.iter().try_fold(0_u64, |total, (name, value)| {
        checked_wire_add(total, name, value)
    })
}

fn overflow() -> PolicyError {
    policy_error(
        PolicyCode::ArithmeticOverflow,
        "header byte accounting overflowed",
    )
}
