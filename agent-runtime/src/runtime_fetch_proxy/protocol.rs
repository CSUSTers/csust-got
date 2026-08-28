use super::{RuntimeFetchProxyError, output, state_error};
use crate::fetch_protocol::{
    FETCH_PROTOCOL_VERSION, FetchRequestHead, MAX_BODY_FRAME_BYTES, MAX_METADATA_BYTES,
};
use serde::{Deserialize, Serialize};

pub const MAX_COMMAND_CONTROL_PACKET_BYTES: usize = MAX_METADATA_BYTES;

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct CommandControlPacket {
    pub protocol_version: u16,
    pub request: FetchRequestHead,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub output_path: Option<String>,
}

impl CommandControlPacket {
    pub fn validate(&self) -> Result<(), RuntimeFetchProxyError> {
        if self.protocol_version != FETCH_PROTOCOL_VERSION
            || self.request.protocol_version != FETCH_PROTOCOL_VERSION
        {
            return Err(RuntimeFetchProxyError::protocol(
                "unsupported command-control protocol version".to_string(),
            ));
        }
        if let Some(path) = &self.output_path {
            output::validate_workspace_output_path(path)?;
        }
        Ok(())
    }
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum LocalRequestState {
    #[default]
    AwaitingContinue,
    Streaming,
    Ended,
    Canceled,
}

impl LocalRequestState {
    pub fn continued(&mut self) -> Result<(), RuntimeFetchProxyError> {
        if *self != Self::AwaitingContinue {
            return Err(state_error("duplicate or out-of-order Continue"));
        }
        *self = Self::Streaming;
        Ok(())
    }

    pub fn body_chunk(&mut self, bytes: usize) -> Result<(), RuntimeFetchProxyError> {
        if *self != Self::Streaming {
            return Err(state_error(
                "body chunk is outside the streaming request state",
            ));
        }
        if bytes > MAX_BODY_FRAME_BYTES {
            return Err(state_error("body chunk exceeds the local frame limit"));
        }
        Ok(())
    }

    pub fn body_end(&mut self) -> Result<(), RuntimeFetchProxyError> {
        if *self != Self::Streaming {
            return Err(state_error("body end is duplicate or out of order"));
        }
        *self = Self::Ended;
        Ok(())
    }

    pub fn cancel(&mut self) -> Result<(), RuntimeFetchProxyError> {
        if matches!(*self, Self::Ended | Self::Canceled) {
            return Err(state_error("cancel is duplicate or follows request end"));
        }
        *self = Self::Canceled;
        Ok(())
    }
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum LocalResponseState {
    #[default]
    AwaitingHead,
    Streaming,
    Ended,
    Failed,
}

impl LocalResponseState {
    pub fn response_head(&mut self) -> Result<(), RuntimeFetchProxyError> {
        if *self != Self::AwaitingHead {
            return Err(state_error("response head is duplicate or out of order"));
        }
        *self = Self::Streaming;
        Ok(())
    }

    pub fn response_chunk(&mut self, bytes: usize) -> Result<(), RuntimeFetchProxyError> {
        if *self != Self::Streaming {
            return Err(state_error(
                "response chunk is outside the streaming response state",
            ));
        }
        if bytes > MAX_BODY_FRAME_BYTES {
            return Err(state_error("response chunk exceeds the local frame limit"));
        }
        Ok(())
    }

    pub fn response_end(&mut self) -> Result<(), RuntimeFetchProxyError> {
        if *self != Self::Streaming {
            return Err(state_error("response end is duplicate or out of order"));
        }
        *self = Self::Ended;
        Ok(())
    }

    pub fn error(&mut self) -> Result<(), RuntimeFetchProxyError> {
        if matches!(*self, Self::Ended | Self::Failed) {
            return Err(state_error("response error follows a terminal frame"));
        }
        *self = Self::Failed;
        Ok(())
    }
}
