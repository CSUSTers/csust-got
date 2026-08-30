mod budget;
mod header;
mod ip;
mod redirect;
mod target;

pub use budget::BudgetTracker;
pub use header::{HeaderPolicy, ReviewedHeaders};
pub use redirect::{BodyReplay, RedirectDecision, RedirectPolicy};
pub use target::{
    ApprovedTarget, NeedsFreshResolution, Origin, RetryTarget, ReviewedTarget, TargetHost,
    TargetPolicy,
};

use ipnet::IpNet;
use std::{error::Error, fmt};

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PolicyConfig {
    pub deny_cidrs: Vec<IpNet>,
    pub credential_header_names: Vec<String>,
    pub request_header_bytes: u64,
    pub request_body_bytes: u64,
    pub response_header_bytes: u64,
    pub response_network_bytes: u64,
    pub response_decoded_bytes: u64,
    pub max_decompression_ratio: u64,
    pub max_redirects: u8,
}

impl PolicyConfig {
    pub fn approved_defaults() -> Self {
        Self {
            deny_cidrs: Vec::new(),
            credential_header_names: Vec::new(),
            request_header_bytes: 32 * 1024,
            request_body_bytes: 8 * 1024 * 1024,
            response_header_bytes: 32 * 1024,
            response_network_bytes: 16 * 1024 * 1024,
            response_decoded_bytes: 32 * 1024 * 1024,
            max_decompression_ratio: 20,
            max_redirects: 5,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum PolicyCode {
    InvalidUrl,
    UnsupportedScheme,
    AmbiguousAuthority,
    InvalidHost,
    InvalidPort,
    EmptyAnswers,
    RestrictedAddress,
    AnswerMismatch,
    InvalidHeader,
    ForbiddenHeader,
    BudgetExceeded,
    DecompressionRatioExceeded,
    ArithmeticOverflow,
    TooManyRedirects,
    HttpsDowngrade,
    BodyNotReplayable,
    UnsupportedRedirectStatus,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PolicyError {
    code: PolicyCode,
    message: &'static str,
}

impl PolicyError {
    pub fn code(&self) -> PolicyCode {
        self.code
    }

    pub fn message(&self) -> &'static str {
        self.message
    }
}

impl fmt::Display for PolicyError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(self.message)
    }
}

impl Error for PolicyError {}

pub(crate) fn policy_error(code: PolicyCode, message: &'static str) -> PolicyError {
    PolicyError { code, message }
}
