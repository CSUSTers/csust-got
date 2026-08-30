use super::{COMMAND_CONTROL_CANCEL_GRACE, RuntimeFetchProxyError, RuntimeFetchProxyErrorCategory};
use crate::fetch_protocol::{
    ErrorCode, FETCH_PROTOCOL_VERSION, FetchProtocolErrorFrame, LocalRuntimeFrame,
    write_local_runtime_frame,
};
use tokio::io::AsyncWrite;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) enum TerminalDelivery {
    Delivered,
    Unavailable,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) struct SessionTaskReceipt {
    pub(super) terminal: TerminalDelivery,
}

pub(super) type SessionTaskResult = Result<SessionTaskReceipt, RuntimeFetchProxyError>;

pub(super) struct LocalTerminalWriter<W> {
    writer: W,
    terminal: Option<TerminalDelivery>,
}

impl<W> LocalTerminalWriter<W>
where
    W: AsyncWrite + Unpin,
{
    pub(super) fn new(writer: W) -> Self {
        Self {
            writer,
            terminal: None,
        }
    }

    pub(super) async fn send_nonterminal(
        &mut self,
        frame: &LocalRuntimeFrame,
    ) -> Result<(), RuntimeFetchProxyError> {
        if self.terminal.is_some() {
            return Err(RuntimeFetchProxyError::protocol(
                "local terminal was already sent".to_string(),
            ));
        }
        bounded_write(&mut self.writer, frame).await
    }

    pub(super) async fn send_terminal(&mut self, frame: &LocalRuntimeFrame) -> SessionTaskReceipt {
        if let Some(terminal) = self.terminal {
            return SessionTaskReceipt { terminal };
        }
        let delivery = if bounded_write(&mut self.writer, frame).await.is_ok() {
            TerminalDelivery::Delivered
        } else {
            TerminalDelivery::Unavailable
        };
        self.terminal = Some(delivery);
        SessionTaskReceipt { terminal: delivery }
    }

    pub(super) async fn send_proxy_error(
        &mut self,
        error: &RuntimeFetchProxyError,
    ) -> SessionTaskReceipt {
        self.send_terminal(&local_error_from_proxy(error)).await
    }
}

async fn bounded_write<W: AsyncWrite + Unpin>(
    writer: &mut W,
    frame: &LocalRuntimeFrame,
) -> Result<(), RuntimeFetchProxyError> {
    match tokio::time::timeout(
        COMMAND_CONTROL_CANCEL_GRACE,
        write_local_runtime_frame(writer, frame),
    )
    .await
    {
        Ok(result) => result.map_err(super::session::protocol_error),
        Err(_) => Err(RuntimeFetchProxyError::timeout(
            "local terminal write timed out",
        )),
    }
}

fn local_error_from_proxy(error: &RuntimeFetchProxyError) -> LocalRuntimeFrame {
    let (code, message) = match error.category() {
        RuntimeFetchProxyErrorCategory::Auth => (ErrorCode::Auth, "fetch unavailable"),
        RuntimeFetchProxyErrorCategory::Policy => (ErrorCode::Policy, "fetch output rejected"),
        RuntimeFetchProxyErrorCategory::Network => (ErrorCode::Network, "fetch network failed"),
        RuntimeFetchProxyErrorCategory::Timeout => (ErrorCode::Timeout, "fetch timed out"),
        RuntimeFetchProxyErrorCategory::Protocol => (ErrorCode::Protocol, "fetch protocol failed"),
        RuntimeFetchProxyErrorCategory::Internal => (ErrorCode::Internal, "fetch output failed"),
    };
    LocalRuntimeFrame::Error(FetchProtocolErrorFrame {
        protocol_version: FETCH_PROTOCOL_VERSION,
        code,
        message: message.to_string(),
    })
}

#[cfg(test)]
mod tests;
