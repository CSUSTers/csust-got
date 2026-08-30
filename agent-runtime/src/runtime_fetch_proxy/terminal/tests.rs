use super::*;
use crate::fetch_protocol::{ErrorCode, LocalRuntimeFrame, read_local_runtime_frame};
use tokio::io::{AsyncWriteExt as _, duplex};

#[tokio::test]
async fn terminal_writer_sends_timeout_once_and_records_unavailable_writer() {
    let (writer, mut reader) = duplex(1024);
    let mut terminal = LocalTerminalWriter::new(writer);
    let error = RuntimeFetchProxyError::timeout("injected timeout");
    assert_eq!(
        terminal.send_proxy_error(&error).await.terminal,
        TerminalDelivery::Delivered
    );
    assert_eq!(
        terminal.send_proxy_error(&error).await.terminal,
        TerminalDelivery::Delivered
    );
    assert!(matches!(
        read_local_runtime_frame(&mut reader).await.unwrap(),
        LocalRuntimeFrame::Error(frame) if frame.code == ErrorCode::Timeout
    ));
    drop(terminal);
    assert!(read_local_runtime_frame(&mut reader).await.is_err());

    let (writer, reader) = duplex(8);
    drop(reader);
    let mut terminal = LocalTerminalWriter::new(writer);
    assert_eq!(
        terminal.send_proxy_error(&error).await.terminal,
        TerminalDelivery::Unavailable
    );
    let _ = terminal.writer.shutdown().await;
}
