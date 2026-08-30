use super::super::{
    RuntimeFetchProxyError,
    terminal::{LocalTerminalWriter, TerminalDelivery},
};
use crate::fetch_protocol::{
    ErrorCode, FETCH_PROTOCOL_VERSION, FetchProtocolErrorFrame, LocalRuntimeFrame,
    read_local_runtime_frame,
};
use tokio::io::duplex;

pub(super) struct TerminalWriterEvidence {
    pub(super) exactly_once: bool,
    pub(super) unavailable: bool,
}

pub(super) async fn terminal_writer_evidence() -> TerminalWriterEvidence {
    let (writer, mut reader) = duplex(1024);
    let mut terminal = LocalTerminalWriter::new(writer);
    let broker_error = LocalRuntimeFrame::Error(FetchProtocolErrorFrame {
        protocol_version: FETCH_PROTOCOL_VERSION,
        code: ErrorCode::Policy,
        message: "denied".to_string(),
    });
    let first = terminal.send_terminal(&broker_error).await;
    let later = terminal
        .send_proxy_error(&RuntimeFetchProxyError::new("later internal failure"))
        .await;
    let frame = read_local_runtime_frame(&mut reader).await.unwrap();
    drop(terminal);
    let exactly_once = first.terminal == TerminalDelivery::Delivered
        && later.terminal == TerminalDelivery::Delivered
        && matches!(frame, LocalRuntimeFrame::Error(error) if error.code == ErrorCode::Policy)
        && read_local_runtime_frame(&mut reader).await.is_err();

    let (writer, reader) = duplex(8);
    drop(reader);
    let mut unavailable_writer = LocalTerminalWriter::new(writer);
    let unavailable = unavailable_writer
        .send_proxy_error(&RuntimeFetchProxyError::new("unavailable writer"))
        .await
        .terminal
        == TerminalDelivery::Unavailable
        && unavailable_writer
            .send_proxy_error(&RuntimeFetchProxyError::new("later failure"))
            .await
            .terminal
            == TerminalDelivery::Unavailable;
    TerminalWriterEvidence {
        exactly_once,
        unavailable,
    }
}
