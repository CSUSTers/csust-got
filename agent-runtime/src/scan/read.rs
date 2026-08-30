use std::{
    io::{self, Read},
    sync::atomic::{AtomicBool, Ordering},
};

pub const READ_CHUNK_BYTES: usize = 8 * 1024;
pub const TRUNCATION_MARKER: &str = "\n[truncated]";

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct BoundedText {
    pub text: String,
    pub truncated: bool,
}

pub fn read_text_bounded<R: Read>(reader: &mut R, output_limit: usize) -> io::Result<BoundedText> {
    let cancel = AtomicBool::new(false);
    read_text_bounded_cancellable(reader, output_limit, &cancel)
}

pub fn read_text_bounded_cancellable<R: Read>(
    reader: &mut R,
    output_limit: usize,
    cancel: &AtomicBool,
) -> io::Result<BoundedText> {
    let retained_limit = output_limit.saturating_add(4);
    let mut bytes = Vec::with_capacity(retained_limit.min(READ_CHUNK_BYTES));
    let mut buffer = [0_u8; READ_CHUNK_BYTES];

    loop {
        let valid_prefix = valid_prefix_len(&bytes)?;
        if cancel.load(Ordering::Acquire) {
            return Ok(truncated_prefix(&bytes, valid_prefix, output_limit));
        }
        if bytes.len() > output_limit && valid_prefix >= output_limit {
            return Ok(truncated_prefix(&bytes, valid_prefix, output_limit));
        }
        if bytes.len() == retained_limit {
            return Ok(truncated_prefix(&bytes, valid_prefix, output_limit));
        }

        let remaining = retained_limit - bytes.len();
        let read_len = buffer.len().min(remaining);
        let read = reader.read(&mut buffer[..read_len])?;
        if read == 0 {
            if valid_prefix != bytes.len() {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidData,
                    "input is not valid UTF-8",
                ));
            }
            let text = String::from_utf8(bytes).expect("validated UTF-8 must convert");
            return Ok(if text.len() > output_limit {
                truncated_prefix(text.as_bytes(), text.len(), output_limit)
            } else {
                BoundedText {
                    text,
                    truncated: false,
                }
            });
        }
        bytes.extend_from_slice(&buffer[..read]);
    }
}

pub(crate) fn finish_bounded_text(text: String, truncated: bool) -> BoundedText {
    if !truncated {
        return BoundedText {
            text,
            truncated: false,
        };
    }
    let mut text = text;
    if !text.ends_with(TRUNCATION_MARKER) {
        text.push_str(TRUNCATION_MARKER);
    }
    BoundedText {
        text,
        truncated: true,
    }
}

fn valid_prefix_len(bytes: &[u8]) -> io::Result<usize> {
    match std::str::from_utf8(bytes) {
        Ok(_) => Ok(bytes.len()),
        Err(error) if error.error_len().is_none() => Ok(error.valid_up_to()),
        Err(_) => Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "input is not valid UTF-8",
        )),
    }
}

fn truncated_prefix(bytes: &[u8], valid_prefix: usize, output_limit: usize) -> BoundedText {
    let valid = std::str::from_utf8(&bytes[..valid_prefix]).expect("validated UTF-8 must decode");
    let mut end = valid.len().min(output_limit);
    while !valid.is_char_boundary(end) {
        end -= 1;
    }
    finish_bounded_text(valid[..end].to_string(), true)
}
