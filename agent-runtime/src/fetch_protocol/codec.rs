use super::*;
use serde::{Deserialize, Serialize};
use tokio::io::{AsyncRead, AsyncReadExt as _, AsyncWrite, AsyncWriteExt as _};

const FRAME_HEADER_BYTES: usize = 5;

pub async fn write_client_frame<W: AsyncWrite + Unpin>(
    writer: &mut W,
    frame: &ClientFrame,
) -> Result<(), ProtocolError> {
    let (kind, payload) = match frame {
        ClientFrame::Hello(value) => metadata(CLIENT_HELLO, value)?,
        ClientFrame::Auth(value) => metadata(CLIENT_AUTH, value)?,
        ClientFrame::Request(value) => {
            validate_headers(&value.headers)?;
            metadata(CLIENT_REQUEST, value)?
        }
        ClientFrame::BodyChunk(bytes) => (CLIENT_BODY_CHUNK, body(bytes)?),
        ClientFrame::BodyEnd => (CLIENT_BODY_END, Vec::new()),
        ClientFrame::Cancel => (CLIENT_CANCEL, Vec::new()),
        ClientFrame::Probe(value) => metadata(CLIENT_PROBE, value)?,
    };
    write_raw(writer, kind, &payload).await
}

pub async fn write_broker_frame<W: AsyncWrite + Unpin>(
    writer: &mut W,
    frame: &BrokerFrame,
) -> Result<(), ProtocolError> {
    let (kind, payload) = match frame {
        BrokerFrame::Hello(value) => metadata(BROKER_HELLO, value)?,
        BrokerFrame::Authenticated => (BROKER_AUTHENTICATED, Vec::new()),
        BrokerFrame::Continue => (BROKER_CONTINUE, Vec::new()),
        BrokerFrame::ResponseHead(value) => {
            validate_headers(&value.headers)?;
            metadata(BROKER_RESPONSE_HEAD, value)?
        }
        BrokerFrame::ResponseChunk(bytes) => (BROKER_RESPONSE_CHUNK, body(bytes)?),
        BrokerFrame::ResponseEnd(value) => metadata(BROKER_RESPONSE_END, value)?,
        BrokerFrame::Error(value) => {
            validate_error_text(&value.message)?;
            metadata(BROKER_ERROR, value)?
        }
        BrokerFrame::Ready(value) => metadata(BROKER_READY, value)?,
    };
    write_raw(writer, kind, &payload).await
}

pub async fn read_client_frame<R: AsyncRead + Unpin>(
    reader: &mut R,
) -> Result<ClientFrame, ProtocolError> {
    let (kind, payload) = read_raw(reader, true).await?;
    match kind {
        CLIENT_HELLO => decode_versioned(&payload).map(ClientFrame::Hello),
        CLIENT_AUTH => decode_versioned(&payload).map(ClientFrame::Auth),
        CLIENT_REQUEST => {
            let value: FetchRequestHead = decode_versioned(&payload)?;
            validate_headers(&value.headers)?;
            Ok(ClientFrame::Request(value))
        }
        CLIENT_BODY_CHUNK => Ok(ClientFrame::BodyChunk(Bytes::from(payload))),
        CLIENT_BODY_END => empty(payload, ClientFrame::BodyEnd),
        CLIENT_CANCEL => empty(payload, ClientFrame::Cancel),
        CLIENT_PROBE => decode_versioned(&payload).map(ClientFrame::Probe),
        _ => Err(ProtocolError::UnknownFrame(kind)),
    }
}

pub async fn read_broker_frame<R: AsyncRead + Unpin>(
    reader: &mut R,
) -> Result<BrokerFrame, ProtocolError> {
    let (kind, payload) = read_raw(reader, false).await?;
    match kind {
        BROKER_HELLO => decode_versioned(&payload).map(BrokerFrame::Hello),
        BROKER_AUTHENTICATED => empty(payload, BrokerFrame::Authenticated),
        BROKER_CONTINUE => empty(payload, BrokerFrame::Continue),
        BROKER_RESPONSE_HEAD => {
            let value: FetchResponseHead = decode_versioned(&payload)?;
            validate_headers(&value.headers)?;
            Ok(BrokerFrame::ResponseHead(value))
        }
        BROKER_RESPONSE_CHUNK => Ok(BrokerFrame::ResponseChunk(Bytes::from(payload))),
        BROKER_RESPONSE_END => decode_versioned(&payload).map(BrokerFrame::ResponseEnd),
        BROKER_ERROR => {
            let value: FetchProtocolErrorFrame = decode_versioned(&payload)?;
            validate_error_text(&value.message)?;
            Ok(BrokerFrame::Error(value))
        }
        BROKER_READY => decode_versioned(&payload).map(BrokerFrame::Ready),
        _ => Err(ProtocolError::UnknownFrame(kind)),
    }
}

pub async fn write_local_client_frame<W: AsyncWrite + Unpin>(
    writer: &mut W,
    frame: &LocalClientFrame,
) -> Result<(), ProtocolError> {
    let (kind, payload) = match frame {
        LocalClientFrame::BodyChunk(bytes) => (LOCAL_BODY_CHUNK, body(bytes)?),
        LocalClientFrame::BodyEnd => (LOCAL_BODY_END, Vec::new()),
        LocalClientFrame::Cancel => (LOCAL_CANCEL, Vec::new()),
    };
    write_raw(writer, kind, &payload).await
}

pub async fn read_local_client_frame<R: AsyncRead + Unpin>(
    reader: &mut R,
) -> Result<LocalClientFrame, ProtocolError> {
    let (kind, payload) = read_local_raw(reader, true).await?;
    match kind {
        LOCAL_BODY_CHUNK => Ok(LocalClientFrame::BodyChunk(Bytes::from(payload))),
        LOCAL_BODY_END => empty(payload, LocalClientFrame::BodyEnd),
        LOCAL_CANCEL => empty(payload, LocalClientFrame::Cancel),
        _ => Err(ProtocolError::UnknownFrame(kind)),
    }
}

pub async fn write_local_runtime_frame<W: AsyncWrite + Unpin>(
    writer: &mut W,
    frame: &LocalRuntimeFrame,
) -> Result<(), ProtocolError> {
    let (kind, payload) = match frame {
        LocalRuntimeFrame::Continue => (LOCAL_CONTINUE, Vec::new()),
        LocalRuntimeFrame::ResponseHead(value) => {
            validate_headers(&value.headers)?;
            metadata(LOCAL_RESPONSE_HEAD, value)?
        }
        LocalRuntimeFrame::ResponseChunk(bytes) => (LOCAL_RESPONSE_CHUNK, body(bytes)?),
        LocalRuntimeFrame::ResponseEnd(value) => metadata(LOCAL_RESPONSE_END, value)?,
        LocalRuntimeFrame::Error(value) => {
            validate_error_text(&value.message)?;
            metadata(LOCAL_ERROR, value)?
        }
    };
    write_raw(writer, kind, &payload).await
}

pub async fn read_local_runtime_frame<R: AsyncRead + Unpin>(
    reader: &mut R,
) -> Result<LocalRuntimeFrame, ProtocolError> {
    let (kind, payload) = read_local_raw(reader, false).await?;
    match kind {
        LOCAL_CONTINUE => empty(payload, LocalRuntimeFrame::Continue),
        LOCAL_RESPONSE_HEAD => {
            let value: FetchResponseHead = decode_versioned(&payload)?;
            validate_headers(&value.headers)?;
            Ok(LocalRuntimeFrame::ResponseHead(value))
        }
        LOCAL_RESPONSE_CHUNK => Ok(LocalRuntimeFrame::ResponseChunk(Bytes::from(payload))),
        LOCAL_RESPONSE_END => decode_versioned(&payload).map(LocalRuntimeFrame::ResponseEnd),
        LOCAL_ERROR => {
            let value: FetchProtocolErrorFrame = decode_versioned(&payload)?;
            validate_error_text(&value.message)?;
            Ok(LocalRuntimeFrame::Error(value))
        }
        _ => Err(ProtocolError::UnknownFrame(kind)),
    }
}

fn decode_versioned<T: for<'de> Deserialize<'de> + Versioned>(
    payload: &[u8],
) -> Result<T, ProtocolError> {
    let value: T = serde_json::from_slice(payload).map_err(|_| ProtocolError::MalformedMetadata)?;
    if value.version() != FETCH_PROTOCOL_VERSION {
        return Err(ProtocolError::UnsupportedVersion(value.version()));
    }
    Ok(value)
}

fn metadata<T: Serialize>(kind: u8, value: &T) -> Result<(u8, Vec<u8>), ProtocolError> {
    let payload = serde_json::to_vec(value).map_err(|_| ProtocolError::MalformedMetadata)?;
    if payload.len() > MAX_METADATA_BYTES {
        return Err(ProtocolError::MetadataTooLarge {
            size: payload.len(),
            limit: MAX_METADATA_BYTES,
        });
    }
    Ok((kind, payload))
}

fn body(bytes: &Bytes) -> Result<Vec<u8>, ProtocolError> {
    if bytes.len() > MAX_BODY_FRAME_BYTES {
        return Err(ProtocolError::FrameTooLarge {
            size: bytes.len(),
            limit: MAX_BODY_FRAME_BYTES,
        });
    }
    Ok(bytes.to_vec())
}

fn validate_headers(headers: &[(String, String)]) -> Result<(), ProtocolError> {
    let size = headers
        .iter()
        .map(|(name, value)| name.len() + value.len() + 4)
        .sum();
    if size > MAX_METADATA_BYTES {
        Err(ProtocolError::MetadataTooLarge {
            size,
            limit: MAX_METADATA_BYTES,
        })
    } else {
        Ok(())
    }
}

fn validate_error_text(message: &str) -> Result<(), ProtocolError> {
    if message.len() > MAX_ERROR_TEXT_BYTES {
        Err(ProtocolError::ErrorTextTooLarge {
            size: message.len(),
            limit: MAX_ERROR_TEXT_BYTES,
        })
    } else {
        Ok(())
    }
}

fn empty<T>(payload: Vec<u8>, value: T) -> Result<T, ProtocolError> {
    if payload.is_empty() {
        Ok(value)
    } else {
        Err(ProtocolError::UnexpectedPayload)
    }
}

async fn write_raw<W: AsyncWrite + Unpin>(
    writer: &mut W,
    kind: u8,
    payload: &[u8],
) -> Result<(), ProtocolError> {
    writer.write_all(&[kind]).await?;
    writer
        .write_all(&(payload.len() as u32).to_be_bytes())
        .await?;
    writer.write_all(payload).await?;
    Ok(())
}

async fn read_raw<R: AsyncRead + Unpin>(
    reader: &mut R,
    client: bool,
) -> Result<(u8, Vec<u8>), ProtocolError> {
    let mut header = [0_u8; FRAME_HEADER_BYTES];
    reader.read_exact(&mut header).await?;
    let kind = header[0];
    let known = if client {
        (CLIENT_HELLO..=CLIENT_PROBE).contains(&kind)
    } else {
        (BROKER_HELLO..=BROKER_READY).contains(&kind)
    };
    if !known {
        return Err(ProtocolError::UnknownFrame(kind));
    }
    let size = u32::from_be_bytes(header[1..].try_into().unwrap()) as usize;
    let limit = if kind == CLIENT_BODY_CHUNK || kind == BROKER_RESPONSE_CHUNK {
        MAX_BODY_FRAME_BYTES
    } else {
        MAX_METADATA_BYTES
    };
    if size > limit {
        return Err(ProtocolError::FrameTooLarge { size, limit });
    }
    let mut payload = vec![0; size];
    reader.read_exact(&mut payload).await?;
    Ok((kind, payload))
}

async fn read_local_raw<R: AsyncRead + Unpin>(
    reader: &mut R,
    client: bool,
) -> Result<(u8, Vec<u8>), ProtocolError> {
    let mut header = [0_u8; FRAME_HEADER_BYTES];
    reader.read_exact(&mut header).await?;
    let kind = header[0];
    let known = if client {
        (LOCAL_BODY_CHUNK..=LOCAL_CANCEL).contains(&kind)
    } else {
        (LOCAL_CONTINUE..=LOCAL_ERROR).contains(&kind)
    };
    if !known {
        return Err(ProtocolError::UnknownFrame(kind));
    }
    let size = u32::from_be_bytes(header[1..].try_into().unwrap()) as usize;
    let limit = if kind == LOCAL_BODY_CHUNK || kind == LOCAL_RESPONSE_CHUNK {
        MAX_BODY_FRAME_BYTES
    } else {
        MAX_METADATA_BYTES
    };
    if size > limit {
        return Err(ProtocolError::FrameTooLarge { size, limit });
    }
    let mut payload = vec![0; size];
    reader.read_exact(&mut payload).await?;
    Ok((kind, payload))
}
