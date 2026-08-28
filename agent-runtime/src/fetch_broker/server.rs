use super::{
    BrokerConfig, ConfigError, DedicatedResolver, PinnedConnector, PinnedHttpClient, Resolver,
    admission::PreAuthAdmission, session, transport::BodyQueueMetrics,
};
use crate::{
    audit::{AuditError, AuditSink, JsonlAuditSink},
    fetch_auth::{ProbeAuthenticator, QuotaRegistry, TokenVerifier},
    fetch_policy::{HeaderPolicy, RedirectPolicy, TargetPolicy},
};
use std::{
    fmt,
    sync::{
        Arc,
        atomic::{AtomicU64, Ordering},
    },
    time::Duration,
};
use tokio::{
    io::{AsyncRead, AsyncWrite, AsyncWriteExt as _},
    sync::Semaphore,
    time::Instant,
};
use zeroize::Zeroize;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct PeerCred {
    pub uid: u32,
    pub gid: u32,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct BrokerMetricsSnapshot {
    pub active_pre_auth: u64,
    pub rejected_pre_auth: u64,
    pub handshake_timeouts: u64,
    pub body_frames_read: u64,
    pub resolver_calls: u64,
    pub connector_calls: u64,
    pub queued_body_frames: u64,
    pub max_queued_body_frames: u64,
}

#[derive(Default)]
pub(crate) struct BrokerMetrics {
    pub(crate) active_pre_auth: Arc<AtomicU64>,
    pub(crate) rejected_pre_auth: AtomicU64,
    pub(crate) handshake_timeouts: AtomicU64,
    pub(crate) body_frames_read: AtomicU64,
    pub(crate) resolver_calls: AtomicU64,
    pub(crate) connector_calls: AtomicU64,
    pub(crate) body_queue: Arc<BodyQueueMetrics>,
}

impl BrokerMetrics {
    pub(crate) fn snapshot(&self) -> BrokerMetricsSnapshot {
        BrokerMetricsSnapshot {
            active_pre_auth: self.active_pre_auth.load(Ordering::Relaxed),
            rejected_pre_auth: self.rejected_pre_auth.load(Ordering::Relaxed),
            handshake_timeouts: self.handshake_timeouts.load(Ordering::Relaxed),
            body_frames_read: self.body_frames_read.load(Ordering::Relaxed),
            resolver_calls: self.resolver_calls.load(Ordering::Relaxed),
            connector_calls: self.connector_calls.load(Ordering::Relaxed),
            queued_body_frames: self.body_queue.queued_frames(),
            max_queued_body_frames: self.body_queue.max_queued_frames(),
        }
    }
}

pub struct BrokerState<R, C, A> {
    pub(crate) config: BrokerConfig,
    pub(crate) resolver: R,
    pub(crate) connector: C,
    pub(crate) audit: A,
    pub(crate) verifier: TokenVerifier,
    pub(crate) probe_authenticator: ProbeAuthenticator,
    pub(crate) quotas: QuotaRegistry,
    pub(crate) targets: TargetPolicy,
    pub(crate) headers: HeaderPolicy,
    pub(crate) redirects: RedirectPolicy,
    pub(crate) metrics: BrokerMetrics,
    pre_auth: Arc<Semaphore>,
}

impl<R, C, A> BrokerState<R, C, A> {
    pub fn new(
        config: BrokerConfig,
        signing_key: impl AsRef<[u8]>,
        resolver: R,
        connector: C,
        audit: A,
    ) -> Result<Self, BrokerError> {
        let policy = config.policy_config();
        let signing_key = signing_key.as_ref();
        let verifier = TokenVerifier::new(signing_key, config.auth_caps())
            .map_err(|_| BrokerError::Configuration("broker signing key is invalid"))?;
        let probe_authenticator = ProbeAuthenticator::new(signing_key)
            .map_err(|_| BrokerError::Configuration("broker signing key is invalid"))?;
        let pre_auth = Arc::new(Semaphore::new(config.pre_auth_connections));
        Ok(Self {
            config,
            resolver,
            connector,
            audit,
            verifier,
            probe_authenticator,
            quotas: QuotaRegistry::new(Duration::from_secs(60)),
            targets: TargetPolicy::new(policy.clone()),
            headers: HeaderPolicy::new(policy.clone()),
            redirects: RedirectPolicy::new(policy),
            metrics: BrokerMetrics::default(),
            pre_auth,
        })
    }

    pub(crate) fn admit_pre_auth(&self, accepted_at: Instant) -> Option<PreAuthAdmission> {
        PreAuthAdmission::try_acquire(
            &self.pre_auth,
            &self.metrics,
            accepted_at,
            self.config.handshake_timeout,
        )
    }
}

pub struct FetchBroker<R = DedicatedResolver, C = PinnedHttpClient, A = JsonlAuditSink> {
    state: Arc<BrokerState<R, C, A>>,
}

impl<R, C, A> FetchBroker<R, C, A> {
    pub fn with_components(
        config: BrokerConfig,
        signing_key: impl AsRef<[u8]>,
        resolver: R,
        connector: C,
        audit: A,
    ) -> Result<Self, BrokerError> {
        Ok(Self {
            state: Arc::new(BrokerState::new(
                config,
                signing_key,
                resolver,
                connector,
                audit,
            )?),
        })
    }

    pub fn metrics(&self) -> BrokerMetricsSnapshot {
        self.state.metrics.snapshot()
    }

    pub fn state(&self) -> &Arc<BrokerState<R, C, A>> {
        &self.state
    }
}

impl<R, C, A> FetchBroker<R, C, A>
where
    R: Resolver + 'static,
    C: PinnedConnector + 'static,
    A: AuditSink + 'static,
{
    pub async fn serve_connection<S>(&self, stream: S, peer: PeerCred) -> Result<(), BrokerError>
    where
        S: AsyncRead + AsyncWrite + Unpin + Send,
    {
        serve_connection(stream, peer, Arc::clone(&self.state)).await
    }

    #[cfg(target_os = "linux")]
    pub async fn serve(&self) -> Result<(), BrokerError> {
        use tokio::net::UnixListener;

        prepare_socket_path(&self.state.config.socket_path)?;
        let listener =
            UnixListener::bind(&self.state.config.socket_path).map_err(BrokerError::Io)?;
        set_socket_permissions(&self.state.config.socket_path, self.state.config.peer_gid)?;
        let _socket = OwnedSocket::capture(&self.state.config.socket_path)?;
        loop {
            let (mut stream, _) = listener.accept().await.map_err(BrokerError::Io)?;
            let accepted_at = Instant::now();
            let Some(admission) = self.state.admit_pre_auth(accepted_at) else {
                let _ = stream.shutdown().await;
                continue;
            };
            let raw_peer = stream.peer_cred().map_err(BrokerError::Io)?;
            let peer = PeerCred {
                uid: raw_peer.uid(),
                gid: raw_peer.gid(),
            };
            let state = Arc::clone(&self.state);
            tokio::spawn(async move {
                if let Err(error) = session::serve_connection(stream, peer, state, admission).await
                {
                    eprintln!("agent-fetch-broker: connection failed: {error}");
                }
            });
        }
    }

    #[cfg(not(target_os = "linux"))]
    pub async fn serve(&self) -> Result<(), BrokerError> {
        Err(BrokerError::UnsupportedPlatform)
    }
}

impl FetchBroker<DedicatedResolver, PinnedHttpClient, JsonlAuditSink> {
    pub async fn from_config(config: BrokerConfig) -> Result<Self, BrokerError> {
        let mut key = config.load_signing_key().map_err(BrokerError::Config)?;
        let resolver = DedicatedResolver::new(&config.dns_servers, config.dns_timeout);
        let connector = PinnedHttpClient::new(config.connect_timeout);
        let audit = JsonlAuditSink::open(&config.audit_path)
            .await
            .map_err(BrokerError::Audit)?;
        let broker = Self::with_components(config, &key, resolver, connector, audit);
        key.zeroize();
        broker
    }
}

pub async fn serve_connection<S, R, C, A>(
    mut stream: S,
    peer: PeerCred,
    state: Arc<BrokerState<R, C, A>>,
) -> Result<(), BrokerError>
where
    S: AsyncRead + AsyncWrite + Unpin + Send,
    R: Resolver + 'static,
    C: PinnedConnector + 'static,
    A: AuditSink + 'static,
{
    let accepted_at = Instant::now();
    let Some(admission) = state.admit_pre_auth(accepted_at) else {
        let _ = stream.shutdown().await;
        return Ok(());
    };
    session::serve_connection(stream, peer, state, admission).await
}

#[derive(Debug)]
pub enum BrokerError {
    Io(std::io::Error),
    Config(ConfigError),
    Audit(AuditError),
    Configuration(&'static str),
    UnsupportedPlatform,
}

impl fmt::Display for BrokerError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Io(_) => formatter.write_str("broker I/O failed"),
            Self::Config(error) => write!(formatter, "broker configuration failed: {error}"),
            Self::Audit(_) => formatter.write_str("broker audit initialization failed"),
            Self::Configuration(message) => formatter.write_str(message),
            Self::UnsupportedPlatform => {
                formatter.write_str("fetch broker production execution requires Linux")
            }
        }
    }
}

impl std::error::Error for BrokerError {}

#[cfg(target_os = "linux")]
fn prepare_socket_path(path: &std::path::Path) -> Result<(), BrokerError> {
    use std::os::unix::fs::{FileTypeExt as _, MetadataExt as _};

    let metadata = match std::fs::symlink_metadata(path) {
        Ok(metadata) => metadata,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(()),
        Err(error) => return Err(BrokerError::Io(error)),
    };
    if !metadata.file_type().is_socket() || metadata.uid() != unsafe { libc::geteuid() } {
        return Err(BrokerError::Configuration(
            "refusing to remove a socket not owned by this broker",
        ));
    }
    std::fs::remove_file(path).map_err(BrokerError::Io)
}

#[cfg(target_os = "linux")]
fn set_socket_permissions(path: &std::path::Path, group: u32) -> Result<(), BrokerError> {
    use std::{ffi::CString, os::unix::fs::PermissionsExt as _};

    std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o660))
        .map_err(BrokerError::Io)?;
    let path = CString::new(path.as_os_str().as_encoded_bytes())
        .map_err(|_| BrokerError::Configuration("socket path contains an interior NUL"))?;
    if unsafe { libc::chown(path.as_ptr(), u32::MAX, group) } != 0 {
        return Err(BrokerError::Io(std::io::Error::last_os_error()));
    }
    Ok(())
}

#[cfg(target_os = "linux")]
struct OwnedSocket {
    path: std::path::PathBuf,
    device: u64,
    inode: u64,
}

#[cfg(target_os = "linux")]
impl OwnedSocket {
    fn capture(path: &std::path::Path) -> Result<Self, BrokerError> {
        use std::os::unix::fs::MetadataExt as _;
        let metadata = std::fs::symlink_metadata(path).map_err(BrokerError::Io)?;
        Ok(Self {
            path: path.to_path_buf(),
            device: metadata.dev(),
            inode: metadata.ino(),
        })
    }
}

#[cfg(target_os = "linux")]
impl Drop for OwnedSocket {
    fn drop(&mut self) {
        use std::os::unix::fs::{FileTypeExt as _, MetadataExt as _};
        let Ok(metadata) = std::fs::symlink_metadata(&self.path) else {
            return;
        };
        if metadata.file_type().is_socket()
            && metadata.dev() == self.device
            && metadata.ino() == self.inode
            && metadata.uid() == unsafe { libc::geteuid() }
        {
            let _ = std::fs::remove_file(&self.path);
        }
    }
}
