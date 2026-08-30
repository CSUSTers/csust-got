use super::{PolicyCode, PolicyConfig, PolicyError, policy_error};

#[derive(Clone, Debug)]
pub struct BudgetTracker {
    config: PolicyConfig,
    request_headers: u64,
    request_body: u64,
    response_headers: u64,
    response_network: u64,
    response_decoded: u64,
}

impl BudgetTracker {
    pub fn new(config: PolicyConfig) -> Self {
        Self {
            config,
            request_headers: 0,
            request_body: 0,
            response_headers: 0,
            response_network: 0,
            response_decoded: 0,
        }
    }

    pub fn check_request_body_length(&self, bytes: u64) -> Result<(), PolicyError> {
        check_limit(
            bytes,
            self.config.request_body_bytes,
            "request body exceeds the byte limit",
        )
    }

    pub fn record_request_header(&mut self, name: &str, value: &str) -> Result<(), PolicyError> {
        let bytes = header_wire_bytes(name, value)?;
        record_limited(
            &mut self.request_headers,
            bytes,
            self.config.request_header_bytes,
            "request headers exceed the byte limit",
        )
    }

    pub fn record_request_body_chunk(&mut self, bytes: u64) -> Result<(), PolicyError> {
        record_limited(
            &mut self.request_body,
            bytes,
            self.config.request_body_bytes,
            "request body exceeds the byte limit",
        )
    }

    pub fn record_response_header(&mut self, name: &str, value: &str) -> Result<(), PolicyError> {
        let bytes = header_wire_bytes(name, value)?;
        record_limited(
            &mut self.response_headers,
            bytes,
            self.config.response_header_bytes,
            "response headers exceed the byte limit",
        )
    }

    pub fn record_response_network_chunk(&mut self, bytes: u64) -> Result<(), PolicyError> {
        record_limited(
            &mut self.response_network,
            bytes,
            self.config.response_network_bytes,
            "network response exceeds the byte limit",
        )
    }

    pub fn record_response_decoded_chunk(&mut self, bytes: u64) -> Result<(), PolicyError> {
        let next = self.response_decoded.checked_add(bytes).ok_or_else(|| {
            policy_error(
                PolicyCode::ArithmeticOverflow,
                "decoded response byte accounting overflowed",
            )
        })?;
        check_limit(
            next,
            self.config.response_decoded_bytes,
            "decoded response exceeds the byte limit",
        )?;
        if next > 0 && self.response_network == 0 {
            return Err(policy_error(
                PolicyCode::DecompressionRatioExceeded,
                "decoded response has no network bytes",
            ));
        }
        let ratio_limit = self
            .response_network
            .checked_mul(self.config.max_decompression_ratio)
            .ok_or_else(|| {
                policy_error(
                    PolicyCode::ArithmeticOverflow,
                    "decompression ratio accounting overflowed",
                )
            })?;
        if next > ratio_limit {
            return Err(policy_error(
                PolicyCode::DecompressionRatioExceeded,
                "decoded response exceeds the decompression ratio",
            ));
        }
        self.response_decoded = next;
        Ok(())
    }

    pub fn request_header_bytes(&self) -> u64 {
        self.request_headers
    }

    pub fn request_body_bytes(&self) -> u64 {
        self.request_body
    }

    pub fn response_header_bytes(&self) -> u64 {
        self.response_headers
    }

    pub fn response_network_bytes(&self) -> u64 {
        self.response_network
    }

    pub fn response_decoded_bytes(&self) -> u64 {
        self.response_decoded
    }
}

impl Default for BudgetTracker {
    fn default() -> Self {
        Self::new(PolicyConfig::approved_defaults())
    }
}

fn record_limited(
    current: &mut u64,
    bytes: u64,
    limit: u64,
    message: &'static str,
) -> Result<(), PolicyError> {
    let next = current.checked_add(bytes).ok_or_else(|| {
        policy_error(
            PolicyCode::ArithmeticOverflow,
            "resource byte accounting overflowed",
        )
    })?;
    check_limit(next, limit, message)?;
    *current = next;
    Ok(())
}

fn check_limit(value: u64, limit: u64, message: &'static str) -> Result<(), PolicyError> {
    if value > limit {
        Err(policy_error(PolicyCode::BudgetExceeded, message))
    } else {
        Ok(())
    }
}

fn header_wire_bytes(name: &str, value: &str) -> Result<u64, PolicyError> {
    let name = u64::try_from(name.len()).map_err(|_| overflow())?;
    let value = u64::try_from(value.len()).map_err(|_| overflow())?;
    name.checked_add(2)
        .and_then(|total| total.checked_add(value))
        .and_then(|total| total.checked_add(2))
        .ok_or_else(overflow)
}

fn overflow() -> PolicyError {
    policy_error(
        PolicyCode::ArithmeticOverflow,
        "header byte accounting overflowed",
    )
}
