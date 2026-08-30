use super::client::session::c7_server_error;
use crate::fetch_protocol::{ErrorCode, FETCH_PROTOCOL_VERSION, FetchProtocolErrorFrame};

pub fn exit_for_error_code(code: ErrorCode) -> i32 {
    c7_server_error(FetchProtocolErrorFrame {
        protocol_version: FETCH_PROTOCOL_VERSION,
        code,
        message: "c7 terminal".to_string(),
    })
    .exit_code() as i32
}
