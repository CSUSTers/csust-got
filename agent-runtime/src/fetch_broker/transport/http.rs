use super::{BodyStream, ConnectError, PinnedConnector, ReviewedRequest, UpstreamResponse};
use crate::fetch_policy::{ApprovedTarget, TargetHost};
use async_trait::async_trait;
use bytes::Bytes;
use futures_util::StreamExt as _;
use http::{HeaderValue, Request, header};
use http_body_util::{BodyExt as _, StreamBody, combinators::BoxBody};
use hyper::{body::Frame, client::conn::http1};
use hyper_util::rt::TokioIo;
use rustls::{ClientConfig, RootCertStore, pki_types::ServerName};
use std::{fmt, net::SocketAddr, sync::Arc, time::Duration};
use tokio::{
    io::{AsyncRead, AsyncWrite},
    net::TcpStream,
    task::JoinHandle,
    time::timeout,
};
use tokio_rustls::TlsConnector;

#[derive(Clone)]
pub struct PinnedHttpClient {
    connect_timeout: Duration,
    tls: TlsConnector,
}

impl PinnedHttpClient {
    pub fn new(connect_timeout: Duration) -> Self {
        let mut roots = RootCertStore::empty();
        roots.extend(webpki_roots::TLS_SERVER_ROOTS.iter().cloned());
        let config = ClientConfig::builder()
            .with_root_certificates(roots)
            .with_no_client_auth();
        Self {
            connect_timeout,
            tls: TlsConnector::from(Arc::new(config)),
        }
    }
}

impl fmt::Debug for PinnedHttpClient {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str("PinnedHttpClient")
    }
}

#[async_trait]
impl PinnedConnector for PinnedHttpClient {
    async fn execute(
        &self,
        request: ReviewedRequest,
        target: ApprovedTarget,
        body: BodyStream,
    ) -> Result<UpstreamResponse, ConnectError> {
        let request = build_request(request, body)?;
        let address = *target.addresses.first().ok_or(ConnectError::Failed)?;
        if target.reviewed.origin.scheme == "https" {
            let host = target
                .reviewed
                .url
                .host_str()
                .ok_or(ConnectError::Failed)?
                .to_string();
            let server_name = ServerName::try_from(host).map_err(|_| ConnectError::Failed)?;
            let stream = timeout(self.connect_timeout, async {
                let stream = TcpStream::connect(address)
                    .await
                    .map_err(|_| ConnectError::Failed)?;
                verify_peer(&stream, address)?;
                self.tls
                    .connect(server_name, stream)
                    .await
                    .map_err(|_| ConnectError::Failed)
            })
            .await
            .map_err(|_| ConnectError::Timeout)??;
            return execute_http(stream, request).await;
        }
        let stream = timeout(self.connect_timeout, async {
            let stream = TcpStream::connect(address)
                .await
                .map_err(|_| ConnectError::Failed)?;
            verify_peer(&stream, address)?;
            Ok::<_, ConnectError>(stream)
        })
        .await
        .map_err(|_| ConnectError::Timeout)??;
        execute_http(stream, request).await
    }
}

fn verify_peer(stream: &TcpStream, address: SocketAddr) -> Result<(), ConnectError> {
    if stream.peer_addr().map_err(|_| ConnectError::Failed)? != address {
        return Err(ConnectError::PeerMismatch);
    }
    Ok(())
}

fn build_request(
    request: ReviewedRequest,
    body: BodyStream,
) -> Result<Request<BoxBody<Bytes, ConnectError>>, ConnectError> {
    let url = &request.target.url;
    let mut path = url.path().to_string();
    if path.is_empty() {
        path.push('/');
    }
    if let Some(query) = url.query() {
        path.push('?');
        path.push_str(query);
    }
    let mut headers = request.headers.headers;
    let host = match (&request.target.host, url.port()) {
        (TargetHost::Address(std::net::IpAddr::V6(address)), Some(port)) => {
            format!("[{address}]:{port}")
        }
        (TargetHost::Address(address), Some(port)) => format!("{address}:{port}"),
        (TargetHost::Address(address), None) => address.to_string(),
        (TargetHost::Name(host), Some(port)) => format!("{host}:{port}"),
        (TargetHost::Name(host), None) => host.clone(),
    };
    headers.insert(
        header::HOST,
        HeaderValue::try_from(host).map_err(|_| ConnectError::Failed)?,
    );
    headers.insert(header::CONNECTION, HeaderValue::from_static("close"));
    Request::builder()
        .method(request.method)
        .uri(path)
        .body(http_body_util::BodyExt::boxed(StreamBody::new(
            body.map(|chunk| chunk.map(Frame::data)),
        )))
        .map(|mut built| {
            *built.headers_mut() = headers;
            built
        })
        .map_err(|_| ConnectError::Failed)
}

async fn execute_http<S>(
    stream: S,
    request: Request<BoxBody<Bytes, ConnectError>>,
) -> Result<UpstreamResponse, ConnectError>
where
    S: AsyncRead + AsyncWrite + Unpin + Send + 'static,
{
    let (mut sender, connection) = http1::handshake(TokioIo::new(stream))
        .await
        .map_err(|_| ConnectError::Failed)?;
    let driver = ConnectionAbort(tokio::spawn(async move {
        let _ = connection.await;
    }));
    let response = sender
        .send_request(request)
        .await
        .map_err(|_| ConnectError::Failed)?;
    let (parts, body) = response.into_parts();
    Ok(UpstreamResponse {
        status: parts.status,
        reason: parts
            .status
            .canonical_reason()
            .unwrap_or_default()
            .to_string(),
        headers: parts.headers,
        body: Box::pin(futures_util::stream::unfold(
            (body, driver),
            |(mut body, driver)| async move {
                loop {
                    match body.frame().await {
                        Some(Ok(frame)) => match frame.into_data() {
                            Ok(data) => return Some((Ok(data), (body, driver))),
                            Err(_) => continue,
                        },
                        Some(Err(_)) => return Some((Err(ConnectError::Failed), (body, driver))),
                        None => return None,
                    }
                }
            },
        )),
    })
}

struct ConnectionAbort(JoinHandle<()>);

impl Drop for ConnectionAbort {
    fn drop(&mut self) {
        self.0.abort();
    }
}
