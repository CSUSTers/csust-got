use super::{
    NeedsFreshResolution, PolicyCode, PolicyConfig, PolicyError, ReviewedHeaders, ReviewedTarget,
    TargetPolicy, policy_error,
};
use http::{Method, StatusCode};

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum BodyReplay {
    Empty,
    Replayable { bytes: u64 },
    NonReplayable { bytes: Option<u64> },
}

#[derive(Clone, Debug)]
pub struct RedirectDecision {
    pub target: ReviewedTarget,
    pub headers: ReviewedHeaders,
    pub method: Method,
    pub body: BodyReplay,
    pub hops: u8,
    pub resolution: NeedsFreshResolution,
}

#[derive(Clone, Debug)]
pub struct RedirectPolicy {
    targets: TargetPolicy,
    credential_header_names: Vec<String>,
    max_redirects: u8,
}

impl RedirectPolicy {
    pub fn new(config: PolicyConfig) -> Self {
        Self {
            targets: TargetPolicy::new(config.clone()),
            credential_header_names: config.credential_header_names.clone(),
            max_redirects: config.max_redirects,
        }
    }

    #[allow(clippy::too_many_arguments)]
    pub fn review(
        &self,
        current: &ReviewedTarget,
        status: StatusCode,
        location: &str,
        headers: ReviewedHeaders,
        method: Method,
        body: BodyReplay,
        completed_hops: u8,
    ) -> Result<RedirectDecision, PolicyError> {
        if completed_hops >= self.max_redirects {
            return Err(policy_error(
                PolicyCode::TooManyRedirects,
                "redirect hop limit exceeded",
            ));
        }
        let (method, body) = redirect_body_semantics(status, method, body)?;
        let target = self.targets.resolve_redirect(current, location)?;
        if current.origin.scheme == "https" && target.origin.scheme == "http" {
            return Err(policy_error(
                PolicyCode::HttpsDowngrade,
                "HTTPS redirects may not downgrade to HTTP",
            ));
        }
        let headers = if target.origin == current.origin {
            headers
        } else {
            headers.strip_sensitive_with(&self.credential_header_names)?
        };
        let hops = completed_hops.checked_add(1).ok_or_else(|| {
            policy_error(
                PolicyCode::ArithmeticOverflow,
                "redirect hop accounting overflowed",
            )
        })?;
        Ok(RedirectDecision {
            target,
            headers,
            method,
            body,
            hops,
            resolution: NeedsFreshResolution,
        })
    }
}

impl Default for RedirectPolicy {
    fn default() -> Self {
        Self::new(PolicyConfig::approved_defaults())
    }
}

fn redirect_body_semantics(
    status: StatusCode,
    method: Method,
    body: BodyReplay,
) -> Result<(Method, BodyReplay), PolicyError> {
    match status {
        StatusCode::SEE_OTHER => {
            let method = if method == Method::HEAD {
                Method::HEAD
            } else {
                Method::GET
            };
            Ok((method, BodyReplay::Empty))
        }
        StatusCode::MOVED_PERMANENTLY | StatusCode::FOUND if method == Method::POST => {
            Ok((Method::GET, BodyReplay::Empty))
        }
        StatusCode::MOVED_PERMANENTLY | StatusCode::FOUND => require_replayable(method, body),
        StatusCode::TEMPORARY_REDIRECT | StatusCode::PERMANENT_REDIRECT => {
            require_replayable(method, body)
        }
        _ => Err(policy_error(
            PolicyCode::UnsupportedRedirectStatus,
            "status code does not define a supported redirect",
        )),
    }
}

fn require_replayable(
    method: Method,
    body: BodyReplay,
) -> Result<(Method, BodyReplay), PolicyError> {
    match body {
        BodyReplay::NonReplayable { bytes: Some(0) } => Ok((method, BodyReplay::Empty)),
        BodyReplay::NonReplayable { .. } => Err(policy_error(
            PolicyCode::BodyNotReplayable,
            "redirect requires a replayable request body",
        )),
        body => Ok((method, body)),
    }
}
