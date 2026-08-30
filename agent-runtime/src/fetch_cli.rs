#[cfg(unix)]
mod body;
#[cfg(all(feature = "c7-test-support", target_os = "linux"))]
pub mod c7_test_support;
mod client;
mod expression;
mod parser;
#[cfg(unix)]
mod workspace_io;

use http::{HeaderName, HeaderValue, Method};
use serde_json::Value;
use std::{fmt, path::PathBuf, time::Duration};

pub use client::run_fetch;

pub const MAX_REQUEST_BODY_BYTES: usize = 8 * 1024 * 1024;
pub const EXIT_USAGE: i32 = FetchExit::Usage as i32;
pub const EXIT_AUTH: i32 = FetchExit::Unavailable as i32;
pub const EXIT_NETWORK_PROTOCOL: i32 = FetchExit::Unavailable as i32;
pub const EXIT_STATUS: i32 = FetchExit::HttpStatus as i32;
pub const EXIT_OUTPUT_IO: i32 = FetchExit::Internal as i32;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(i32)]
pub enum FetchExit {
    Success = 0,
    Usage = 2,
    HttpStatus = 22,
    Timeout = 28,
    Policy = 65,
    Unavailable = 69,
    Internal = 70,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum FetchErrorKind {
    Usage,
    Policy,
    Auth,
    Timeout,
    NetworkProtocol,
    Status,
    OutputIo,
    BrokenPipe,
}

#[derive(Debug)]
pub struct FetchError {
    kind: FetchErrorKind,
    message: String,
}

impl FetchError {
    pub fn new(kind: FetchErrorKind, message: impl Into<String>) -> Self {
        Self {
            kind,
            message: message.into(),
        }
    }

    pub fn kind(&self) -> FetchErrorKind {
        self.kind
    }

    pub fn exit_code(&self) -> FetchExit {
        match self.kind {
            FetchErrorKind::Usage => FetchExit::Usage,
            FetchErrorKind::Policy => FetchExit::Policy,
            FetchErrorKind::Auth | FetchErrorKind::NetworkProtocol => FetchExit::Unavailable,
            FetchErrorKind::Timeout => FetchExit::Timeout,
            FetchErrorKind::Status => FetchExit::HttpStatus,
            FetchErrorKind::OutputIo => FetchExit::Internal,
            FetchErrorKind::BrokenPipe => FetchExit::Success,
        }
    }

    pub fn is_broken_pipe(&self) -> bool {
        self.kind == FetchErrorKind::BrokenPipe
    }
}

impl fmt::Display for FetchError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(&self.message)
    }
}

impl std::error::Error for FetchError {}

#[derive(Clone, Debug, PartialEq)]
pub struct JsonField {
    pub name: String,
    pub value: Value,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct FormField {
    pub name: String,
    pub value: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum FormPart {
    Field(FormField),
    File { name: String, path: PathBuf },
}

#[derive(Clone, Debug, PartialEq)]
pub enum BodySource {
    Empty,
    Json(Vec<JsonField>),
    Form(Vec<FormField>),
    Multipart(Vec<FormPart>),
    RawFile(PathBuf),
    RawStdin,
}

#[derive(Clone, Debug)]
pub struct FetchCli {
    pub method: Method,
    pub url: String,
    pub headers: Vec<(HeaderName, HeaderValue)>,
    pub body: BodySource,
    pub follow: bool,
    pub show_headers: bool,
    pub check_status: bool,
    pub output: Option<PathBuf>,
    pub timeout: Option<Duration>,
}

impl FetchCli {
    pub fn parse<I, S>(argv: I) -> Result<Self, FetchError>
    where
        I: IntoIterator<Item = S>,
        S: AsRef<str>,
    {
        parser::parse(argv)
    }
}

fn usage(message: impl Into<String>) -> FetchError {
    FetchError::new(FetchErrorKind::Usage, message)
}
