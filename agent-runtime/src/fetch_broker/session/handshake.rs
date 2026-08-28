use super::{outcome::Failure, outcome::protocol_io, reject, request::verify_auth};
use crate::{
    audit::{AuditHealth, AuditSink},
    fetch_auth::VerifiedClaims,
    fetch_broker::{BrokerError, BrokerState, PeerCred},
    fetch_protocol::{
        BrokerFrame, ClientFrame, FETCH_PROTOCOL_VERSION, read_client_frame, write_broker_frame,
    },
};
use tokio::io::{AsyncRead, AsyncWrite};

pub(super) enum PreAuthOutcome {
    Authenticated(VerifiedClaims),
    Finished,
}

pub(super) async fn authenticate<Reader, Writer, R, C, A>(
    reader: &mut Reader,
    writer: &mut Writer,
    peer: PeerCred,
    state: &BrokerState<R, C, A>,
) -> Result<PreAuthOutcome, BrokerError>
where
    Reader: AsyncRead + Unpin,
    Writer: AsyncWrite + Unpin,
    A: AuditSink,
{
    if peer.uid != state.config.peer_uid || peer.gid != state.config.peer_gid {
        reject(writer, Failure::Auth).await?;
        return Ok(PreAuthOutcome::Finished);
    }
    match read_client_frame(reader).await {
        Ok(ClientFrame::Probe(probe)) => {
            serve_probe(writer, probe, state).await?;
            return Ok(PreAuthOutcome::Finished);
        }
        Ok(ClientFrame::Hello(hello)) if hello.protocol_version == FETCH_PROTOCOL_VERSION => {}
        _ => {
            reject(writer, Failure::Protocol).await?;
            return Ok(PreAuthOutcome::Finished);
        }
    }
    write_broker_frame(
        writer,
        &BrokerFrame::Hello(crate::fetch_protocol::BrokerHello {
            protocol_version: FETCH_PROTOCOL_VERSION,
        }),
    )
    .await
    .map_err(protocol_io)?;
    let auth = match read_client_frame(reader).await {
        Ok(ClientFrame::Auth(auth)) => auth,
        _ => {
            reject(writer, Failure::Protocol).await?;
            return Ok(PreAuthOutcome::Finished);
        }
    };
    let claims = match verify_auth(state, &auth) {
        Ok(claims) => claims,
        Err(failure) => {
            reject(writer, failure).await?;
            return Ok(PreAuthOutcome::Finished);
        }
    };
    write_broker_frame(writer, &BrokerFrame::Authenticated)
        .await
        .map_err(protocol_io)?;
    Ok(PreAuthOutcome::Authenticated(claims))
}

async fn serve_probe<W, R, C, A>(
    writer: &mut W,
    probe: crate::fetch_protocol::FetchProbe,
    state: &BrokerState<R, C, A>,
) -> Result<(), BrokerError>
where
    W: AsyncWrite + Unpin,
    A: AuditSink,
{
    if probe.protocol_version != FETCH_PROTOCOL_VERSION
        || probe.policy_version != state.config.policy_version
    {
        return reject(writer, Failure::Policy).await;
    }
    if state.probe_authenticator.verify_probe(&probe).is_err()
        || state.audit.health() != AuditHealth::Healthy
    {
        return reject(writer, Failure::Auth).await;
    }
    let ready = state.probe_authenticator.create_ready(&probe);
    write_broker_frame(writer, &BrokerFrame::Ready(ready))
        .await
        .map_err(protocol_io)
}
