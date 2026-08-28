use crate::exec::COMMAND_CONTROL_FD;
use crate::{
    config::FetchClaimLimits,
    fetch_auth::{FetchClaims, ProbeAuthenticator, TokenIssuer},
    fetch_protocol::{
        BrokerFrame, ClientFrame, SecretString, read_broker_frame, write_client_frame,
    },
};
use std::{
    fmt,
    path::{Path, PathBuf},
    sync::Arc,
    time::{Duration, SystemTime, UNIX_EPOCH},
};
use tokio::io::{AsyncRead, AsyncWrite};

pub const SHELL_PATH: &str = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin";
const TOKEN_GRACE: Duration = Duration::from_secs(10);

#[derive(Clone)]
pub struct RuntimeFetchSecurity {
    socket_path: PathBuf,
    limits: FetchClaimLimits,
    issuer: Arc<TokenIssuer>,
    probe_authenticator: Arc<ProbeAuthenticator>,
}

pub struct IssuedFetchCommand {
    pub claims: FetchClaims,
    pub token: SecretString,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum RuntimeSecurityError {
    InvalidSigningKey,
    Clock,
    InvalidTimeout,
    Randomness,
    IssueToken,
    BrokerUnavailable,
}

impl RuntimeFetchSecurity {
    pub fn new(
        socket_path: impl AsRef<Path>,
        signing_key: impl AsRef<[u8]>,
        limits: FetchClaimLimits,
    ) -> Result<Self, RuntimeSecurityError> {
        let signing_key = signing_key.as_ref();
        let issuer =
            TokenIssuer::new(signing_key).map_err(|_| RuntimeSecurityError::InvalidSigningKey)?;
        let probe_authenticator = ProbeAuthenticator::new(signing_key)
            .map_err(|_| RuntimeSecurityError::InvalidSigningKey)?;
        Ok(Self {
            socket_path: socket_path.as_ref().to_path_buf(),
            limits,
            issuer: Arc::new(issuer),
            probe_authenticator: Arc::new(probe_authenticator),
        })
    }

    pub fn issue_command(
        &self,
        namespace: &str,
        run_id: &str,
        effective_timeout: Duration,
        now: SystemTime,
    ) -> Result<IssuedFetchCommand, RuntimeSecurityError> {
        let command_id = self.new_command_id()?;
        self.issue_for_command(namespace, run_id, command_id, effective_timeout, now)
    }

    pub fn new_command_id(&self) -> Result<String, RuntimeSecurityError> {
        random_command_id()
    }

    pub fn issue_for_command(
        &self,
        namespace: &str,
        run_id: &str,
        command_id: String,
        effective_timeout: Duration,
        now: SystemTime,
    ) -> Result<IssuedFetchCommand, RuntimeSecurityError> {
        if effective_timeout.is_zero() {
            return Err(RuntimeSecurityError::InvalidTimeout);
        }
        let issued_at_unix = i64::try_from(
            now.duration_since(UNIX_EPOCH)
                .map_err(|_| RuntimeSecurityError::Clock)?
                .as_secs(),
        )
        .map_err(|_| RuntimeSecurityError::Clock)?;
        let lifetime = effective_timeout
            .checked_add(TOKEN_GRACE)
            .ok_or(RuntimeSecurityError::InvalidTimeout)?;
        let lifetime =
            i64::try_from(lifetime.as_secs()).map_err(|_| RuntimeSecurityError::InvalidTimeout)?;
        let expires_at_unix = issued_at_unix
            .checked_add(lifetime)
            .ok_or(RuntimeSecurityError::InvalidTimeout)?;
        let claims = FetchClaims {
            protocol_version: self.limits.protocol_version,
            policy_version: self.limits.policy_version.clone(),
            namespace: namespace.to_string(),
            run_id: run_id.to_string(),
            command_id,
            issued_at_unix,
            expires_at_unix,
            max_concurrency: self.limits.max_concurrency,
            max_requests: self.limits.max_requests,
            max_request_bytes: self.limits.max_request_bytes,
            max_response_bytes: self.limits.max_response_bytes,
        };
        let token = self
            .issuer
            .issue(&claims)
            .map_err(|_| RuntimeSecurityError::IssueToken)?;
        Ok(IssuedFetchCommand { claims, token })
    }

    pub fn shell_environment(&self) -> Vec<(String, String)> {
        vec![
            ("PATH".to_string(), SHELL_PATH.to_string()),
            ("HOME".to_string(), "/tmp".to_string()),
            (
                "AGENT_FETCH_CONTROL_FD".to_string(),
                COMMAND_CONTROL_FD.to_string(),
            ),
        ]
    }

    pub fn socket_path(&self) -> &Path {
        &self.socket_path
    }

    pub fn policy_version(&self) -> &str {
        &self.limits.policy_version
    }

    pub async fn probe_stream<S>(&self, stream: &mut S) -> Result<(), RuntimeSecurityError>
    where
        S: AsyncRead + AsyncWrite + Unpin,
    {
        let mut nonce = [0_u8; 16];
        getrandom::fill(&mut nonce).map_err(|_| RuntimeSecurityError::Randomness)?;
        let probe = self.probe_authenticator.create_probe(
            self.limits.protocol_version,
            &self.limits.policy_version,
            nonce,
        );
        write_client_frame(stream, &ClientFrame::Probe(probe))
            .await
            .map_err(|_| RuntimeSecurityError::BrokerUnavailable)?;
        let ready = match read_broker_frame(stream)
            .await
            .map_err(|_| RuntimeSecurityError::BrokerUnavailable)?
        {
            BrokerFrame::Ready(ready) => ready,
            _ => return Err(RuntimeSecurityError::BrokerUnavailable),
        };
        if ready.protocol_version != self.limits.protocol_version
            || ready.policy_version != self.limits.policy_version
            || ready.nonce != nonce
            || self.probe_authenticator.verify_ready(&ready).is_err()
        {
            return Err(RuntimeSecurityError::BrokerUnavailable);
        }
        Ok(())
    }

    pub async fn probe(&self) -> bool {
        #[cfg(unix)]
        {
            let result = tokio::time::timeout(Duration::from_secs(1), async {
                let mut stream = tokio::net::UnixStream::connect(&self.socket_path)
                    .await
                    .map_err(|_| RuntimeSecurityError::BrokerUnavailable)?;
                self.probe_stream(&mut stream).await
            })
            .await;
            matches!(result, Ok(Ok(())))
        }
        #[cfg(not(unix))]
        {
            false
        }
    }
}

impl fmt::Debug for RuntimeFetchSecurity {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter
            .debug_struct("RuntimeFetchSecurity")
            .field("socket_path", &self.socket_path)
            .field("limits", &self.limits)
            .field("issuer", &"[REDACTED]")
            .finish()
    }
}

impl fmt::Debug for IssuedFetchCommand {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter
            .debug_struct("IssuedFetchCommand")
            .field("claims", &self.claims)
            .field("token", &"[REDACTED]")
            .finish()
    }
}

impl fmt::Display for RuntimeSecurityError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(match self {
            Self::InvalidSigningKey => "runtime fetch signing key is invalid",
            Self::Clock => "runtime clock cannot issue fetch credentials",
            Self::InvalidTimeout => "effective command timeout cannot issue fetch credentials",
            Self::Randomness => "runtime could not create a command identity",
            Self::IssueToken => "runtime could not issue fetch credentials",
            Self::BrokerUnavailable => "fetch broker readiness probe failed",
        })
    }
}

impl std::error::Error for RuntimeSecurityError {}

fn random_command_id() -> Result<String, RuntimeSecurityError> {
    let mut bytes = [0_u8; 16];
    getrandom::fill(&mut bytes).map_err(|_| RuntimeSecurityError::Randomness)?;
    let mut output = String::with_capacity(32);
    for byte in bytes {
        use std::fmt::Write as _;
        write!(&mut output, "{byte:02x}").expect("writing to a String cannot fail");
    }
    Ok(output)
}
