use super::types::{AuthError, AuthErrorKind};
use sha2::{Digest, Sha256};
use std::fmt;
use zeroize::Zeroize;

const HMAC_BLOCK_BYTES: usize = 64;
const HMAC_SHA256_BYTES: usize = 32;
const MIN_SIGNING_KEY_BYTES: usize = 32;

#[derive(Clone)]
pub(super) struct SigningKey {
    bytes: [u8; HMAC_BLOCK_BYTES],
}

impl SigningKey {
    pub(super) fn new(key: impl AsRef<[u8]>) -> Result<Self, AuthError> {
        let key = key.as_ref();
        if key.len() < MIN_SIGNING_KEY_BYTES {
            return Err(AuthError::new(AuthErrorKind::InvalidSigningKey));
        }

        let mut bytes = [0_u8; HMAC_BLOCK_BYTES];
        if key.len() > HMAC_BLOCK_BYTES {
            bytes[..HMAC_SHA256_BYTES].copy_from_slice(&Sha256::digest(key));
        } else {
            bytes[..key.len()].copy_from_slice(key);
        }
        Ok(Self { bytes })
    }

    pub(super) fn sign(&self, message: &[u8]) -> [u8; HMAC_SHA256_BYTES] {
        let mut inner_pad = [0_u8; HMAC_BLOCK_BYTES];
        let mut outer_pad = [0_u8; HMAC_BLOCK_BYTES];
        for (index, key_byte) in self.bytes.iter().copied().enumerate() {
            inner_pad[index] = key_byte ^ 0x36;
            outer_pad[index] = key_byte ^ 0x5c;
        }

        let mut inner = Sha256::new();
        inner.update(inner_pad);
        inner.update(message);

        let mut outer = Sha256::new();
        outer.update(outer_pad);
        outer.update(inner.finalize());

        let mut signature = [0_u8; HMAC_SHA256_BYTES];
        signature.copy_from_slice(&outer.finalize());
        signature
    }
}

impl fmt::Debug for SigningKey {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str("SigningKey([REDACTED])")
    }
}

impl Drop for SigningKey {
    fn drop(&mut self) {
        self.bytes.zeroize();
    }
}

pub(super) fn canonical_base64url_decode(value: &str) -> Result<Vec<u8>, AuthError> {
    let decoded =
        base64url_decode(value).ok_or_else(|| AuthError::new(AuthErrorKind::MalformedToken))?;
    if base64url_encode(&decoded) != value {
        return Err(AuthError::new(AuthErrorKind::MalformedToken));
    }
    Ok(decoded)
}

pub(super) fn base64url_encode(bytes: &[u8]) -> String {
    const ALPHABET: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";

    let mut encoded = String::with_capacity(bytes.len().div_ceil(3) * 4);
    for chunk in bytes.chunks(3) {
        let first = chunk[0];
        encoded.push(ALPHABET[(first >> 2) as usize] as char);
        match chunk.len() {
            1 => {
                encoded.push(ALPHABET[((first & 0x03) << 4) as usize] as char);
            }
            2 => {
                let second = chunk[1];
                encoded.push(ALPHABET[(((first & 0x03) << 4) | (second >> 4)) as usize] as char);
                encoded.push(ALPHABET[((second & 0x0f) << 2) as usize] as char);
            }
            3 => {
                let second = chunk[1];
                let third = chunk[2];
                encoded.push(ALPHABET[(((first & 0x03) << 4) | (second >> 4)) as usize] as char);
                encoded.push(ALPHABET[(((second & 0x0f) << 2) | (third >> 6)) as usize] as char);
                encoded.push(ALPHABET[(third & 0x3f) as usize] as char);
            }
            _ => unreachable!("chunks(3) cannot yield more than three bytes"),
        }
    }
    encoded
}

pub(super) fn base64url_decode(value: &str) -> Option<Vec<u8>> {
    let bytes = value.as_bytes();
    if bytes.len() % 4 == 1 {
        return None;
    }

    let mut decoded = Vec::with_capacity(bytes.len().div_ceil(4) * 3);
    let full_chunks = bytes.len() / 4;
    let remainder = bytes.len() % 4;
    let full_end = full_chunks * 4;

    for chunk in bytes[..full_end].chunks_exact(4) {
        let a = base64url_value(chunk[0])?;
        let b = base64url_value(chunk[1])?;
        let c = base64url_value(chunk[2])?;
        let d = base64url_value(chunk[3])?;
        decoded.push((a << 2) | (b >> 4));
        decoded.push((b << 4) | (c >> 2));
        decoded.push((c << 6) | d);
    }

    match remainder {
        0 => {}
        2 => {
            let a = base64url_value(bytes[full_end])?;
            let b = base64url_value(bytes[full_end + 1])?;
            if b & 0x0f != 0 {
                return None;
            }
            decoded.push((a << 2) | (b >> 4));
        }
        3 => {
            let a = base64url_value(bytes[full_end])?;
            let b = base64url_value(bytes[full_end + 1])?;
            let c = base64url_value(bytes[full_end + 2])?;
            if c & 0x03 != 0 {
                return None;
            }
            decoded.push((a << 2) | (b >> 4));
            decoded.push((b << 4) | (c >> 2));
        }
        _ => return None,
    }
    Some(decoded)
}

fn base64url_value(byte: u8) -> Option<u8> {
    match byte {
        b'A'..=b'Z' => Some(byte - b'A'),
        b'a'..=b'z' => Some(byte - b'a' + 26),
        b'0'..=b'9' => Some(byte - b'0' + 52),
        b'-' => Some(62),
        b'_' => Some(63),
        _ => None,
    }
}

pub(super) fn constant_time_eq(expected: &[u8], supplied: &[u8]) -> bool {
    let mut difference = expected.len() ^ supplied.len();
    for (index, expected_byte) in expected.iter().copied().enumerate() {
        difference |= usize::from(expected_byte ^ supplied.get(index).copied().unwrap_or_default());
    }
    difference == 0
}
