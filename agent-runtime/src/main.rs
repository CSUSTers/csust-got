use agent_runtime::{
    AppState, BashSandboxMode, app,
    config::{RuntimeConfig, RuntimeFetchConfig},
    exec::{BashHealth, CommandSupervisor},
    namespace_gate::NamespaceGate,
    runtime_fetch_proxy::RuntimeFetchProxy,
    runtime_security::RuntimeFetchSecurity,
    workspace_budget::WorkspaceBudget,
};
use std::env;
#[cfg(target_os = "linux")]
use std::path::PathBuf;
use tokio::net::TcpListener;
use tracing_subscriber::{EnvFilter, fmt};
use zeroize::Zeroize;

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    ensure_production_platform()?;
    agent_runtime::sandbox::harden_runtime_supervisor()?;
    init_tracing();

    let config = RuntimeConfig::from_env(|name| env::var(name).ok())?;
    let workspace_budget =
        WorkspaceBudget::new(&config.workspace_root, config.workspace_max_bytes)?;
    let (fetch_proxy, require_fetch_for_readiness) = match &config.fetch {
        RuntimeFetchConfig::Disabled => {
            (RuntimeFetchProxy::disabled(workspace_budget.clone()), false)
        }
        RuntimeFetchConfig::Enabled {
            socket_path,
            limits,
            require_for_readiness,
            ..
        } => {
            let mut signing_key = config
                .fetch
                .load_signing_key()?
                .expect("enabled fetch configuration has a signing key");
            let security = RuntimeFetchSecurity::new(socket_path, &signing_key, limits.clone())?;
            signing_key.zeroize();
            (
                RuntimeFetchProxy::enabled(security, workspace_budget.clone()),
                *require_for_readiness,
            )
        }
    };
    let (command_supervisor, bash_readiness_error) = production_command_supervisor(&config).await;
    let bash_health = command_supervisor
        .as_ref()
        .map(CommandSupervisor::health)
        .unwrap_or_else(|| BashHealth::unavailable(bash_readiness_error.clone()));

    let state = AppState {
        workspace_root: workspace_budget.root().to_path_buf(),
        skills_root: config.skills_root.clone(),
        auth_token: Some(config.auth_token.expose_secret().to_owned()),
        max_output_chars: config.max_output_chars,
        command_timeout: config.command_timeout,
        trace_jsonl_path: config.trace_jsonl_path.clone(),
        bash_sandbox: BashSandboxMode::Proot,
        command_supervisor: command_supervisor.clone(),
        bash_health,
        fetch_proxy: fetch_proxy.clone(),
        require_fetch_for_readiness,
        bash_readiness_error,
        workspace_budget,
        namespace_gate: NamespaceGate::default(),
    };

    let listener = TcpListener::bind(config.listen_addr).await?;
    tracing::info!(addr = %config.listen_addr, "agent runtime listening");
    let shutdown_supervisor = command_supervisor.clone();
    let result = axum::serve(listener, app(state))
        .with_graceful_shutdown(async move {
            shutdown_signal().await;
            if let Some(supervisor) = shutdown_supervisor
                && let Err(error) = supervisor.shutdown().await
            {
                tracing::error!(%error, "command shutdown completed with cleanup failure");
            }
        })
        .await;
    let command_shutdown = match &command_supervisor {
        Some(supervisor) => supervisor.shutdown().await,
        None => Ok(()),
    };
    if let Err(error) = command_shutdown {
        tracing::error!(%error, "command requests returned with cleanup pending");
    }
    let proxy_shutdown = fetch_proxy.shutdown().await;
    let stale_recovery = recover_only_after_proxy_shutdown(proxy_shutdown, || async {
        match &command_supervisor {
            Some(supervisor) => supervisor.recover_stale().await,
            None => Ok(()),
        }
    })
    .await;
    result?;
    stale_recovery?;
    Ok(())
}

async fn recover_only_after_proxy_shutdown<ProxyError, RecoveryError, Recovery, RecoveryFuture>(
    proxy_shutdown: Result<(), ProxyError>,
    recovery: Recovery,
) -> anyhow::Result<()>
where
    ProxyError: std::error::Error + Send + Sync + 'static,
    RecoveryError: std::error::Error + Send + Sync + 'static,
    Recovery: FnOnce() -> RecoveryFuture,
    RecoveryFuture: std::future::Future<Output = Result<(), RecoveryError>>,
{
    proxy_shutdown.map_err(anyhow::Error::new)?;
    recovery().await.map_err(anyhow::Error::new)
}

async fn shutdown_signal() {
    #[cfg(target_os = "linux")]
    {
        use tokio::signal::unix::{SignalKind, signal};

        let mut terminate = match signal(SignalKind::terminate()) {
            Ok(signal) => signal,
            Err(error) => {
                tracing::error!(%error, "failed to install SIGTERM shutdown handler");
                return;
            }
        };
        tokio::select! {
            result = tokio::signal::ctrl_c() => {
                if let Err(error) = result {
                    tracing::error!(%error, "failed to receive Ctrl-C shutdown signal");
                }
            }
            _ = terminate.recv() => {}
        }
    }
    #[cfg(not(target_os = "linux"))]
    {
        let _ = tokio::signal::ctrl_c().await;
    }
}

fn ensure_production_platform() -> anyhow::Result<()> {
    #[cfg(target_os = "linux")]
    {
        Ok(())
    }
    #[cfg(not(target_os = "linux"))]
    {
        anyhow::bail!("agent runtime production execution requires Linux")
    }
}

async fn production_command_supervisor(
    config: &RuntimeConfig,
) -> (Option<CommandSupervisor>, String) {
    #[cfg(target_os = "linux")]
    {
        if let Err(error) = config.cgroup_topology.validate_runtime() {
            let message = format!("bash unavailable: runtime cgroup topology is invalid: {error}");
            tracing::error!(%error, "bash disabled: runtime cgroup topology validation failed");
            return (None, message);
        }
        let exec_helper = config.exec_helper.clone().unwrap_or_else(|| {
            env::current_exe()
                .unwrap_or_else(|_| PathBuf::from("agent-runtime"))
                .with_file_name("agent-runtime-exec")
        });
        match CommandSupervisor::production_with_rlimits(
            config.cgroup.clone(),
            config.rlimits.clone(),
            exec_helper,
        ) {
            Ok(supervisor) => match supervisor.recover_stale().await {
                Ok(()) => (Some(supervisor), String::new()),
                Err(error) => {
                    tracing::error!(%error, "bash disabled: stale cgroup recovery failed");
                    (
                        None,
                        format!("bash unavailable: stale cgroup recovery failed: {error}"),
                    )
                }
            },
            Err(error) => {
                tracing::error!(%error, "bash disabled: cgroup v2 delegation is not ready");
                (
                    None,
                    format!("bash unavailable: cgroup v2 delegation is not ready: {error}"),
                )
            }
        }
    }
    #[cfg(not(target_os = "linux"))]
    {
        let _ = config;
        (
            None,
            "bash unavailable: agent runtime production execution requires Linux".to_string(),
        )
    }
}

fn init_tracing() {
    let filter = EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info"));
    fmt().with_env_filter(filter).json().init();
}

#[cfg(test)]
mod tests {
    #[cfg(not(target_os = "linux"))]
    use super::ensure_production_platform;
    use super::recover_only_after_proxy_shutdown;
    use std::sync::{
        Arc,
        atomic::{AtomicUsize, Ordering},
    };

    #[cfg(not(target_os = "linux"))]
    #[test]
    fn non_linux_production_start_is_rejected() {
        assert_eq!(
            ensure_production_platform().unwrap_err().to_string(),
            "agent runtime production execution requires Linux"
        );
    }

    #[tokio::test]
    async fn proxy_shutdown_failure_prevents_stale_recovery() {
        let recoveries = Arc::new(AtomicUsize::new(0));
        let observed = Arc::clone(&recoveries);

        let result = recover_only_after_proxy_shutdown(
            Err(std::io::Error::other("redacted proxy shutdown failure")),
            move || async move {
                observed.fetch_add(1, Ordering::SeqCst);
                Ok::<(), std::io::Error>(())
            },
        )
        .await;

        assert!(result.is_err());
        assert_eq!(recoveries.load(Ordering::SeqCst), 0);
    }
}
