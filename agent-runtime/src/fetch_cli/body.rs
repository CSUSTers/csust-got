use super::{
    BodySource, FetchError, FetchErrorKind, FormPart, MAX_REQUEST_BODY_BYTES,
    client::SharedWriter,
    workspace_io::{InputFile, open_input},
};
use crate::fetch_protocol::{LocalClientFrame, MAX_BODY_FRAME_BYTES};
use bytes::Bytes;
use std::io::Read as _;
use tokio::io::{AsyncRead, AsyncReadExt as _};

const FILE_BUFFER_BYTES: usize = 32 * 1024;
const MULTIPART_BOUNDARY: &str = "agent-runtime-fetch-v1";

pub(crate) struct PreparedBody {
    kind: PreparedKind,
    declared_len: Option<u64>,
}

enum PreparedKind {
    Empty,
    Pieces(Vec<Vec<u8>>),
    RawFile(InputFile),
    RawStdin,
    Multipart(Vec<PreparedPart>),
}

enum PreparedPart {
    Bytes(Vec<u8>),
    File(InputFile),
}

impl PreparedBody {
    pub fn prepare(source: BodySource) -> Result<Self, FetchError> {
        let kind = match source {
            BodySource::Empty => PreparedKind::Empty,
            BodySource::Json(fields) => {
                let mut pieces = vec![b"{".to_vec()];
                for (index, field) in fields.into_iter().enumerate() {
                    if index > 0 {
                        pieces.push(b",".to_vec());
                    }
                    pieces.push(serde_json::to_vec(&field.name).map_err(internal)?);
                    pieces.push(b":".to_vec());
                    pieces.push(serde_json::to_vec(&field.value).map_err(internal)?);
                }
                pieces.push(b"}".to_vec());
                PreparedKind::Pieces(pieces)
            }
            BodySource::Form(fields) => {
                let mut pieces = Vec::new();
                for (index, field) in fields.into_iter().enumerate() {
                    if index > 0 {
                        pieces.push(b"&".to_vec());
                    }
                    pieces.push(form_encode(&field.name));
                    pieces.push(b"=".to_vec());
                    pieces.push(form_encode(&field.value));
                }
                PreparedKind::Pieces(pieces)
            }
            BodySource::RawFile(path) => PreparedKind::RawFile(open_input(&path)?),
            BodySource::RawStdin => PreparedKind::RawStdin,
            BodySource::Multipart(parts) => PreparedKind::Multipart(prepare_multipart(parts)?),
        };
        let declared_len = match &kind {
            PreparedKind::Empty => Some(0),
            PreparedKind::Pieces(pieces) => Some(sum_lengths(pieces.iter().map(Vec::len))?),
            PreparedKind::RawFile(file) => Some(file.len),
            PreparedKind::RawStdin => None,
            PreparedKind::Multipart(parts) => {
                Some(sum_lengths(parts.iter().map(|part| match part {
                    PreparedPart::Bytes(bytes) => bytes.len(),
                    PreparedPart::File(file) => file.len as usize,
                }))?)
            }
        };
        Ok(Self { kind, declared_len })
    }

    pub fn declared_len(&self) -> Option<u64> {
        self.declared_len
    }

    pub async fn send<I: AsyncRead + Unpin>(
        mut self,
        stdin: &mut I,
        writer: &SharedWriter,
    ) -> Result<(), FetchError> {
        let mut sent = 0_usize;
        match &mut self.kind {
            PreparedKind::Empty => {}
            PreparedKind::Pieces(pieces) => {
                for piece in pieces {
                    send_piece(writer, piece, &mut sent).await?;
                }
            }
            PreparedKind::RawFile(file) => send_file(writer, file, &mut sent).await?,
            PreparedKind::RawStdin => send_stdin(writer, stdin, &mut sent).await?,
            PreparedKind::Multipart(parts) => {
                for part in parts {
                    match part {
                        PreparedPart::Bytes(bytes) => send_piece(writer, bytes, &mut sent).await?,
                        PreparedPart::File(file) => send_file(writer, file, &mut sent).await?,
                    }
                }
            }
        }
        if self
            .declared_len
            .is_some_and(|declared| declared != sent as u64)
        {
            return Err(policy("input file changed while streaming"));
        }
        writer.send(&LocalClientFrame::BodyEnd).await
    }
}

fn prepare_multipart(parts: Vec<FormPart>) -> Result<Vec<PreparedPart>, FetchError> {
    let mut prepared = Vec::new();
    for part in parts {
        match part {
            FormPart::Field(field) => prepared.push(PreparedPart::Bytes(
                format!(
                    "--{MULTIPART_BOUNDARY}\r\nContent-Disposition: form-data; name=\"{}\"\r\n\r\n{}\r\n",
                    field.name, field.value
                )
                .into_bytes(),
            )),
            FormPart::File { name, path } => {
                let file = open_input(&path)?;
                if file.name.contains(['\r', '\n', '"']) {
                    return Err(policy("invalid upload file name"));
                }
                prepared.push(PreparedPart::Bytes(
                    format!(
                        "--{MULTIPART_BOUNDARY}\r\nContent-Disposition: form-data; name=\"{name}\"; filename=\"{}\"\r\nContent-Type: application/octet-stream\r\n\r\n",
                        file.name
                    )
                    .into_bytes(),
                ));
                prepared.push(PreparedPart::File(file));
                prepared.push(PreparedPart::Bytes(b"\r\n".to_vec()));
            }
        }
    }
    prepared.push(PreparedPart::Bytes(
        format!("--{MULTIPART_BOUNDARY}--\r\n").into_bytes(),
    ));
    Ok(prepared)
}

async fn send_file(
    writer: &SharedWriter,
    file: &mut InputFile,
    sent: &mut usize,
) -> Result<(), FetchError> {
    let mut buffer = [0_u8; FILE_BUFFER_BYTES];
    loop {
        let read = file
            .file
            .read(&mut buffer)
            .map_err(|error| policy(format!("read input file failed: {error}")))?;
        if read == 0 {
            break;
        }
        send_piece(writer, &buffer[..read], sent).await?;
    }
    Ok(())
}

async fn send_stdin<I: AsyncRead + Unpin>(
    writer: &SharedWriter,
    stdin: &mut I,
    sent: &mut usize,
) -> Result<(), FetchError> {
    let mut buffer = [0_u8; FILE_BUFFER_BYTES];
    loop {
        let read = stdin
            .read(&mut buffer)
            .await
            .map_err(|error| policy(format!("read stdin failed: {error}")))?;
        if read == 0 {
            break;
        }
        send_piece(writer, &buffer[..read], sent).await?;
    }
    Ok(())
}

async fn send_piece(
    writer: &SharedWriter,
    bytes: &[u8],
    sent: &mut usize,
) -> Result<(), FetchError> {
    if sent.saturating_add(bytes.len()) > MAX_REQUEST_BODY_BYTES {
        return Err(policy(format!(
            "request body exceeds {} byte limit",
            MAX_REQUEST_BODY_BYTES
        )));
    }
    for chunk in bytes.chunks(MAX_BODY_FRAME_BYTES) {
        writer
            .send(&LocalClientFrame::BodyChunk(Bytes::copy_from_slice(chunk)))
            .await?;
    }
    *sent += bytes.len();
    Ok(())
}

fn sum_lengths(mut lengths: impl Iterator<Item = usize>) -> Result<u64, FetchError> {
    let total = lengths
        .try_fold(0_usize, |total, length| total.checked_add(length))
        .ok_or_else(|| policy("request body length overflow"))?;
    if total > MAX_REQUEST_BODY_BYTES {
        Err(policy(format!(
            "request body exceeds {} byte limit",
            MAX_REQUEST_BODY_BYTES
        )))
    } else {
        Ok(total as u64)
    }
}

fn form_encode(value: &str) -> Vec<u8> {
    let mut encoded = Vec::with_capacity(value.len());
    for byte in value.bytes() {
        match byte {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'.' | b'_' | b'~' => {
                encoded.push(byte)
            }
            b' ' => encoded.push(b'+'),
            _ => encoded.extend_from_slice(format!("%{byte:02X}").as_bytes()),
        }
    }
    encoded
}

fn policy(message: impl Into<String>) -> FetchError {
    FetchError::new(FetchErrorKind::Policy, message)
}

fn internal(error: serde_json::Error) -> FetchError {
    FetchError::new(
        FetchErrorKind::NetworkProtocol,
        format!("encode request metadata failed: {error}"),
    )
}
