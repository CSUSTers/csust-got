use agent_runtime::BashSandboxMode;
use agent_runtime::{AppState, app};
use std::{env, net::SocketAddr, path::PathBuf, time::Duration};
use tokio::net::TcpListener;
use tracing_subscriber::{EnvFilter, fmt};

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    init_tracing();

    let addr: SocketAddr = env::var("AGENT_RUNTIME_ADDR")
        .unwrap_or_else(|_| "0.0.0.0:8080".to_string())
        .parse()?;
    let workspace_root = PathBuf::from(
        env::var("AGENT_RUNTIME_WORKSPACE_ROOT").unwrap_or_else(|_| "workspaces".to_string()),
    );
    let skills_root = PathBuf::from(
        env::var("AGENT_RUNTIME_SKILLS_ROOT").unwrap_or_else(|_| "skills".to_string()),
    );
    let auth_token = env::var("AGENT_RUNTIME_TOKEN")
        .ok()
        .filter(|v| !v.trim().is_empty());
    let max_output_chars = env::var("AGENT_RUNTIME_MAX_OUTPUT_CHARS")
        .ok()
        .and_then(|v| v.parse::<usize>().ok())
        .unwrap_or(12000);
    let command_timeout = env::var("AGENT_RUNTIME_COMMAND_TIMEOUT_SECS")
        .ok()
        .and_then(|v| v.parse::<u64>().ok())
        .map(Duration::from_secs)
        .unwrap_or(Duration::from_secs(120));
    let trace_jsonl_path = PathBuf::from(
        env::var("AGENT_RUNTIME_TRACE_JSONL")
            .unwrap_or_else(|_| "logs/runtime-traces.jsonl".to_string()),
    );
    let bash_sandbox = BashSandboxMode::from_env(
        &env::var("AGENT_RUNTIME_BASH_SANDBOX").unwrap_or_else(|_| "proot".to_string()),
    );

    let state = AppState {
        workspace_root,
        skills_root,
        auth_token,
        max_output_chars,
        command_timeout,
        trace_jsonl_path,
        bash_sandbox,
    };

    let listener = TcpListener::bind(addr).await?;
    tracing::info!(%addr, "agent runtime listening");
    axum::serve(listener, app(state)).await?;
    Ok(())
}

fn init_tracing() {
    let filter = EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info"));
    fmt().with_env_filter(filter).json().init();
}
