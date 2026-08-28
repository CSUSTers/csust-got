mod codec;

pub use codec::{
    read_broker_frame, read_client_frame, read_local_client_frame, read_local_runtime_frame,
    write_broker_frame, write_client_frame, write_local_client_frame, write_local_runtime_frame,
};

use bytes::Bytes;
use serde::{Deserialize, Deserializer, Serialize, Serializer};
use std::{fmt, io};
use zeroize::Zeroize;

pub const FETCH_PROTOCOL_VERSION: u16 = 1;
pub const MAX_METADATA_BYTES: usize = 32 * 1024;
pub const MAX_BODY_FRAME_BYTES: usize = 64 * 1024;
pub const MAX_ERROR_TEXT_BYTES: usize = 4 * 1024;
pub const LOCAL_SESSION_CHANNEL_CAPACITY: usize = 1;

pub(super) const CLIENT_HELLO: u8 = 0x01;
pub(super) const CLIENT_AUTH: u8 = 0x02;
pub(super) const CLIENT_REQUEST: u8 = 0x03;
pub(super) const CLIENT_BODY_CHUNK: u8 = 0x04;
pub(super) const CLIENT_BODY_END: u8 = 0x05;
pub(super) const CLIENT_CANCEL: u8 = 0x06;
pub(super) const CLIENT_PROBE: u8 = 0x07;
pub(super) const BROKER_HELLO: u8 = 0x81;
pub(super) const BROKER_AUTHENTICATED: u8 = 0x82;
pub(super) const BROKER_CONTINUE: u8 = 0x83;
pub(super) const BROKER_RESPONSE_HEAD: u8 = 0x84;
pub(super) const BROKER_RESPONSE_CHUNK: u8 = 0x85;
pub(super) const BROKER_RESPONSE_END: u8 = 0x86;
pub(super) const BROKER_ERROR: u8 = 0x87;
pub(super) const BROKER_READY: u8 = 0x88;
pub(super) const LOCAL_BODY_CHUNK: u8 = 0x21;
pub(super) const LOCAL_BODY_END: u8 = 0x22;
pub(super) const LOCAL_CANCEL: u8 = 0x23;
pub(super) const LOCAL_CONTINUE: u8 = 0xa1;
pub(super) const LOCAL_RESPONSE_HEAD: u8 = 0xa2;
pub(super) const LOCAL_RESPONSE_CHUNK: u8 = 0xa3;
pub(super) const LOCAL_RESPONSE_END: u8 = 0xa4;
pub(super) const LOCAL_ERROR: u8 = 0xa5;

#[derive(Clone, PartialEq, Eq)]
pub struct SecretString(String);

impl SecretString {
    pub fn new(value: impl Into<String>) -> Self {
        Self(value.into())
    }

    pub fn expose_secret(&self) -> &str {
        &self.0
    }
}

impl fmt::Debug for SecretString {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str("SecretString([REDACTED])")
    }
}

impl Drop for SecretString {
    fn drop(&mut self) {
        self.0.zeroize();
    }
}

impl Serialize for SecretString {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: Serializer,
    {
        serializer.serialize_str(&self.0)
    }
}

impl<'de> Deserialize<'de> for SecretString {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        String::deserialize(deserializer).map(Self)
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ClientHello {
    pub protocol_version: u16,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct BrokerHello {
    pub protocol_version: u16,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct AuthMetadata {
    pub protocol_version: u16,
    pub token: SecretString,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct FetchProbe {
    pub protocol_version: u16,
    pub policy_version: String,
    pub nonce: [u8; 16],
    pub mac: [u8; 32],
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct FetchReady {
    pub protocol_version: u16,
    pub policy_version: String,
    pub nonce: [u8; 16],
    pub mac: [u8; 32],
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct FetchRequestHead {
    pub protocol_version: u16,
    pub method: String,
    pub url: String,
    pub headers: Vec<(String, String)>,
    pub follow: bool,
    pub check_status: bool,
    pub timeout_ms: Option<u64>,
    pub declared_body_bytes: Option<u64>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct FetchResponseHead {
    pub protocol_version: u16,
    pub status: u16,
    pub reason: String,
    pub headers: Vec<(String, String)>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct FetchResponseEnd {
    pub protocol_version: u16,
    pub body_bytes: u64,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct LocalResponseEnd {
    pub protocol_version: u16,
    pub body_bytes: u64,
    pub output_committed: bool,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ErrorCode {
    Auth,
    Policy,
    Timeout,
    Network,
    Protocol,
    Internal,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct FetchProtocolErrorFrame {
    pub protocol_version: u16,
    pub code: ErrorCode,
    pub message: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum ClientFrame {
    Hello(ClientHello),
    Auth(AuthMetadata),
    Request(FetchRequestHead),
    BodyChunk(Bytes),
    BodyEnd,
    Cancel,
    Probe(FetchProbe),
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum BrokerFrame {
    Hello(BrokerHello),
    Authenticated,
    Continue,
    ResponseHead(FetchResponseHead),
    ResponseChunk(Bytes),
    ResponseEnd(FetchResponseEnd),
    Error(FetchProtocolErrorFrame),
    Ready(FetchReady),
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum LocalClientFrame {
    BodyChunk(Bytes),
    BodyEnd,
    Cancel,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum LocalRuntimeFrame {
    Continue,
    ResponseHead(FetchResponseHead),
    ResponseChunk(Bytes),
    ResponseEnd(LocalResponseEnd),
    Error(FetchProtocolErrorFrame),
}

#[derive(Debug)]
pub enum ProtocolError {
    Io(io::Error),
    UnknownFrame(u8),
    FrameTooLarge { size: usize, limit: usize },
    MetadataTooLarge { size: usize, limit: usize },
    ErrorTextTooLarge { size: usize, limit: usize },
    MalformedMetadata,
    UnsupportedVersion(u16),
    UnexpectedPayload,
}

impl fmt::Display for ProtocolError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Io(error) => write!(formatter, "protocol I/O failed: {error}"),
            Self::UnknownFrame(kind) => write!(formatter, "unknown protocol frame 0x{kind:02x}"),
            Self::FrameTooLarge { size, limit } => {
                write!(
                    formatter,
                    "protocol frame is {size} bytes; limit is {limit}"
                )
            }
            Self::MetadataTooLarge { size, limit } => {
                write!(
                    formatter,
                    "protocol metadata is {size} bytes; limit is {limit}"
                )
            }
            Self::ErrorTextTooLarge { size, limit } => {
                write!(
                    formatter,
                    "protocol error text is {size} bytes; limit is {limit}"
                )
            }
            Self::MalformedMetadata => formatter.write_str("protocol metadata is malformed"),
            Self::UnsupportedVersion(version) => {
                write!(formatter, "unsupported fetch protocol version {version}")
            }
            Self::UnexpectedPayload => {
                formatter.write_str("protocol frame has an unexpected payload")
            }
        }
    }
}

impl std::error::Error for ProtocolError {}

impl From<io::Error> for ProtocolError {
    fn from(error: io::Error) -> Self {
        Self::Io(error)
    }
}

pub(super) trait Versioned {
    fn version(&self) -> u16;
}

macro_rules! versioned {
    ($($type:ty),+ $(,)?) => {
        $(impl Versioned for $type {
            fn version(&self) -> u16 { self.protocol_version }
        })+
    };
}

versioned!(
    ClientHello,
    BrokerHello,
    AuthMetadata,
    FetchProbe,
    FetchReady,
    FetchRequestHead,
    FetchResponseHead,
    FetchResponseEnd,
    LocalResponseEnd,
    FetchProtocolErrorFrame,
);
