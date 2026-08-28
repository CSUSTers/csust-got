use super::{PolicyCode, PolicyConfig, PolicyError, ip::is_restricted, policy_error};
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr, SocketAddr};
use url::Url;

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct NeedsFreshResolution;

#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub enum TargetHost {
    Name(String),
    Address(IpAddr),
}

#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub struct Origin {
    pub scheme: String,
    pub host: TargetHost,
    pub port: u16,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ReviewedTarget {
    pub url: Url,
    pub origin: Origin,
    pub host: TargetHost,
    pub port: u16,
    pub resolution: NeedsFreshResolution,
}

#[derive(Debug, PartialEq, Eq)]
pub struct ApprovedTarget {
    pub reviewed: ReviewedTarget,
    pub addresses: Vec<SocketAddr>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RetryTarget {
    pub target: ReviewedTarget,
    pub resolution: NeedsFreshResolution,
}

impl ApprovedTarget {
    pub fn into_retry(self) -> RetryTarget {
        RetryTarget {
            target: self.reviewed,
            resolution: NeedsFreshResolution,
        }
    }
}

#[derive(Clone, Debug)]
pub struct TargetPolicy {
    config: PolicyConfig,
}

impl TargetPolicy {
    pub fn new(config: PolicyConfig) -> Self {
        Self { config }
    }

    pub fn normalize(&self, raw: &str) -> Result<ReviewedTarget, PolicyError> {
        validate_reference_text(raw)?;
        let (scheme, authority) = raw_authority(raw)?;
        let host = parse_authority(authority, scheme)?;
        let url = Url::parse(raw)
            .map_err(|_| policy_error(PolicyCode::InvalidUrl, "URL cannot be parsed safely"))?;
        if !url.username().is_empty() || url.password().is_some() {
            return Err(policy_error(
                PolicyCode::AmbiguousAuthority,
                "URL user information is not allowed",
            ));
        }
        if url.fragment().is_some() {
            return Err(policy_error(
                PolicyCode::InvalidUrl,
                "URL fragments are not allowed",
            ));
        }
        let port = url
            .port_or_known_default()
            .ok_or_else(|| policy_error(PolicyCode::InvalidPort, "URL port is not available"))?;
        let parsed_host = parsed_url_host(&url)?;
        if parsed_host != host {
            return Err(policy_error(
                PolicyCode::AmbiguousAuthority,
                "URL parser changed the supplied authority",
            ));
        }
        let origin = Origin {
            scheme: scheme.to_string(),
            host: host.clone(),
            port,
        };
        Ok(ReviewedTarget {
            url,
            origin,
            host,
            port,
            resolution: NeedsFreshResolution,
        })
    }

    pub fn review_answers(
        &self,
        target: ReviewedTarget,
        answers: &[IpAddr],
    ) -> Result<ApprovedTarget, PolicyError> {
        if answers.is_empty() {
            return Err(policy_error(
                PolicyCode::EmptyAnswers,
                "target resolution returned no addresses",
            ));
        }
        if let TargetHost::Address(literal) = &target.host
            && answers.iter().any(|answer| answer != literal)
        {
            return Err(policy_error(
                PolicyCode::AnswerMismatch,
                "literal target does not match reviewed addresses",
            ));
        }
        if answers
            .iter()
            .any(|answer| is_restricted(*answer, &self.config.deny_cidrs))
        {
            return Err(policy_error(
                PolicyCode::RestrictedAddress,
                "target resolves to a restricted address",
            ));
        }
        let mut addresses = answers
            .iter()
            .map(|answer| SocketAddr::new(*answer, target.port))
            .collect::<Vec<_>>();
        addresses.sort_unstable();
        addresses.dedup();
        Ok(ApprovedTarget {
            reviewed: target,
            addresses,
        })
    }

    pub(crate) fn resolve_redirect(
        &self,
        current: &ReviewedTarget,
        location: &str,
    ) -> Result<ReviewedTarget, PolicyError> {
        validate_reference_text(location)?;
        if has_scheme(location) {
            return self.normalize(location);
        }
        if location.starts_with("//") {
            return self.normalize(&format!("{}:{location}", current.origin.scheme));
        }
        let joined = current.url.join(location).map_err(|_| {
            policy_error(
                PolicyCode::InvalidUrl,
                "redirect location cannot be resolved safely",
            )
        })?;
        self.normalize(joined.as_str())
    }
}

impl Default for TargetPolicy {
    fn default() -> Self {
        Self::new(PolicyConfig::approved_defaults())
    }
}

fn validate_reference_text(raw: &str) -> Result<(), PolicyError> {
    if raw
        .chars()
        .any(|character| character == '\\' || character.is_control() || character.is_whitespace())
    {
        return Err(policy_error(
            PolicyCode::InvalidUrl,
            "URL contains forbidden characters",
        ));
    }
    if raw.contains('#') {
        return Err(policy_error(
            PolicyCode::InvalidUrl,
            "URL fragments are not allowed",
        ));
    }
    validate_percent_encoding(raw)
}

fn raw_authority(raw: &str) -> Result<(&str, &str), PolicyError> {
    let separator = raw.find("://").ok_or_else(|| {
        policy_error(
            PolicyCode::InvalidUrl,
            "URL must contain an explicit authority",
        )
    })?;
    let raw_scheme = &raw[..separator];
    let scheme = if raw_scheme.eq_ignore_ascii_case("http") {
        "http"
    } else if raw_scheme.eq_ignore_ascii_case("https") {
        "https"
    } else {
        return Err(policy_error(
            PolicyCode::UnsupportedScheme,
            "only HTTP and HTTPS URLs are allowed",
        ));
    };
    let rest = &raw[separator + 3..];
    let end = rest.find(['/', '?']).unwrap_or(rest.len());
    let authority = &rest[..end];
    if authority.is_empty() {
        return Err(policy_error(
            PolicyCode::InvalidHost,
            "URL host is required",
        ));
    }
    if authority.contains('@') || authority.contains('%') || !authority.is_ascii() {
        return Err(policy_error(
            PolicyCode::AmbiguousAuthority,
            "URL authority is ambiguous",
        ));
    }
    Ok((scheme, authority))
}

fn parse_authority(authority: &str, scheme: &str) -> Result<TargetHost, PolicyError> {
    if let Some(bracketed) = authority.strip_prefix('[') {
        let close = bracketed.find(']').ok_or_else(|| {
            policy_error(PolicyCode::InvalidHost, "IPv6 target must be bracketed")
        })?;
        let raw_host = &bracketed[..close];
        let suffix = &bracketed[close + 1..];
        if !suffix.is_empty() {
            let raw_port = suffix
                .strip_prefix(':')
                .ok_or_else(|| policy_error(PolicyCode::InvalidPort, "URL port is invalid"))?;
            parse_explicit_port(raw_port, scheme)?;
        }
        let address = raw_host
            .parse::<Ipv6Addr>()
            .map_err(|_| policy_error(PolicyCode::InvalidHost, "IPv6 target is invalid"))?;
        if raw_host != address.to_string() {
            return Err(policy_error(
                PolicyCode::InvalidHost,
                "IPv6 target is not canonical",
            ));
        }
        return Ok(TargetHost::Address(IpAddr::V6(address)));
    }

    if authority.contains('[') || authority.contains(']') {
        return Err(policy_error(
            PolicyCode::InvalidHost,
            "URL host brackets are invalid",
        ));
    }
    let (raw_host, raw_port) = match authority.rsplit_once(':') {
        Some((host, port)) => {
            if host.contains(':') {
                return Err(policy_error(
                    PolicyCode::InvalidHost,
                    "IPv6 target must be bracketed",
                ));
            }
            (host, Some(port))
        }
        None => (authority, None),
    };
    if let Some(port) = raw_port {
        parse_explicit_port(port, scheme)?;
    }
    if raw_host.is_empty() {
        return Err(policy_error(
            PolicyCode::InvalidHost,
            "URL host is required",
        ));
    }
    if let Ok(address) = raw_host.parse::<Ipv4Addr>() {
        if raw_host != address.to_string() {
            return Err(policy_error(
                PolicyCode::InvalidHost,
                "IPv4 target is not canonical",
            ));
        }
        return Ok(TargetHost::Address(IpAddr::V4(address)));
    }
    if looks_like_alternate_ipv4(raw_host) {
        return Err(policy_error(
            PolicyCode::InvalidHost,
            "alternate IPv4 spelling is not allowed",
        ));
    }
    validate_dns_name(raw_host)?;
    Ok(TargetHost::Name(raw_host.to_ascii_lowercase()))
}

fn parse_explicit_port(raw: &str, scheme: &str) -> Result<u16, PolicyError> {
    if raw.is_empty()
        || !raw.bytes().all(|byte| byte.is_ascii_digit())
        || (raw.len() > 1 && raw.starts_with('0'))
    {
        return Err(policy_error(
            PolicyCode::InvalidPort,
            "URL port is ambiguous",
        ));
    }
    let port = raw
        .parse::<u16>()
        .map_err(|_| policy_error(PolicyCode::InvalidPort, "URL port is invalid"))?;
    if port == 0 || (scheme == "http" && port == 80) || (scheme == "https" && port == 443) {
        return Err(policy_error(
            PolicyCode::InvalidPort,
            "explicit default or zero URL port is not allowed",
        ));
    }
    Ok(port)
}

fn validate_dns_name(raw: &str) -> Result<(), PolicyError> {
    if raw.len() > 253 || raw.starts_with('.') || raw.ends_with('.') || !raw.contains('.') {
        return Err(policy_error(
            PolicyCode::InvalidHost,
            "DNS target must be a multi-label canonical name",
        ));
    }
    for label in raw.split('.') {
        if label.is_empty()
            || label.len() > 63
            || !label
                .bytes()
                .all(|byte| byte.is_ascii_alphanumeric() || byte == b'-')
            || !label.as_bytes()[0].is_ascii_alphanumeric()
            || !label.as_bytes()[label.len() - 1].is_ascii_alphanumeric()
        {
            return Err(policy_error(
                PolicyCode::InvalidHost,
                "DNS target name is invalid",
            ));
        }
    }
    Ok(())
}

fn looks_like_alternate_ipv4(raw: &str) -> bool {
    raw.bytes()
        .all(|byte| byte.is_ascii_digit() || byte == b'.')
        || raw.split('.').any(|label| {
            label.len() > 2
                && label.as_bytes()[0] == b'0'
                && matches!(label.as_bytes()[1], b'x' | b'X')
        })
}

fn parsed_url_host(url: &Url) -> Result<TargetHost, PolicyError> {
    match url.host() {
        Some(url::Host::Domain(name)) => Ok(TargetHost::Name(name.to_string())),
        Some(url::Host::Ipv4(address)) => Ok(TargetHost::Address(IpAddr::V4(address))),
        Some(url::Host::Ipv6(address)) => Ok(TargetHost::Address(IpAddr::V6(address))),
        None => Err(policy_error(
            PolicyCode::InvalidHost,
            "URL host is required",
        )),
    }
}

fn validate_percent_encoding(raw: &str) -> Result<(), PolicyError> {
    let bytes = raw.as_bytes();
    let mut decoded = Vec::with_capacity(bytes.len());
    let mut index = 0;
    while index < bytes.len() {
        if bytes[index] != b'%' {
            decoded.push(bytes[index]);
            index += 1;
            continue;
        }
        if index + 2 >= bytes.len() {
            return Err(policy_error(
                PolicyCode::InvalidUrl,
                "URL percent encoding is invalid",
            ));
        }
        let high = hex_value(bytes[index + 1]);
        let low = hex_value(bytes[index + 2]);
        let (Some(high), Some(low)) = (high, low) else {
            return Err(policy_error(
                PolicyCode::InvalidUrl,
                "URL percent encoding is invalid",
            ));
        };
        decoded.push((high << 4) | low);
        index += 3;
    }
    if decoded.windows(3).any(|window| {
        window[0] == b'%' && hex_value(window[1]).is_some() && hex_value(window[2]).is_some()
    }) {
        return Err(policy_error(
            PolicyCode::InvalidUrl,
            "double URL encoding is not allowed",
        ));
    }
    Ok(())
}

fn hex_value(byte: u8) -> Option<u8> {
    match byte {
        b'0'..=b'9' => Some(byte - b'0'),
        b'a'..=b'f' => Some(byte - b'a' + 10),
        b'A'..=b'F' => Some(byte - b'A' + 10),
        _ => None,
    }
}

fn has_scheme(raw: &str) -> bool {
    let Some(colon) = raw.find(':') else {
        return false;
    };
    let candidate = &raw[..colon];
    !candidate.is_empty()
        && candidate.as_bytes()[0].is_ascii_alphabetic()
        && candidate
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'+' | b'-' | b'.'))
}
