use axum::{
    Json, Router,
    body::Body,
    extract::State,
    http::{HeaderMap, Request, StatusCode},
    response::{IntoResponse, Response},
    routing::{get, post},
};
use diffy::Patch;
use regex::Regex;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::{
    ffi::OsStr,
    path::{Component, Path, PathBuf},
    sync::Arc,
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};
use tokio::{fs, io::AsyncWriteExt, process::Command, time::timeout};
use tracing::{info, warn};
use walkdir::WalkDir;

#[derive(Clone)]
pub struct AppState {
    pub workspace_root: PathBuf,
    pub skills_root: PathBuf,
    pub auth_token: Option<String>,
    pub max_output_chars: usize,
    pub command_timeout: Duration,
    pub trace_jsonl_path: PathBuf,
    pub bash_sandbox: BashSandboxMode,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum BashSandboxMode {
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
}

#[derive(Debug, Serialize, Deserialize)]
pub struct ResetResponse {
    pub ok: bool,
    pub namespace_hash: String,
    pub removed: bool,
}

#[derive(Debug, Serialize)]
struct TraceRecord {
    run_id: String,
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
    Ok(Json(StatusResponse {
        ok: true,
        version: env!("CARGO_PKG_VERSION").to_string(),
        workspace_root: state.workspace_root.display().to_string(),
        skills_root: state.skills_root.display().to_string(),
        bash_sandbox: state.bash_sandbox.as_str().to_string(),
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
    let path = resolve_virtual_path(state, &req.common, &req.path, AccessMode::Read).await?;
    let data = fs::read_to_string(&path)
        .await
        .map_err(|e| RuntimeError::bad_request(format!("read failed: {e}")))?;
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
    let path = resolve_virtual_path(state, &req.common, target, AccessMode::Read).await?;
    let re = Regex::new(&req.pattern)
        .or_else(|_| Regex::new(&regex::escape(&req.pattern)))
        .map_err(|e| RuntimeError::bad_request(format!("invalid pattern: {e}")))?;
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
    for (idx, line) in data.lines().enumerate() {
        if re.is_match(line) {
            output.push_str(&format!("{display_path}:{}:{line}\n", idx + 1));
        }
    }
    Ok(())
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
    let path = resolve_virtual_path(state, &req.common, &req.path, AccessMode::Write).await?;
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)
            .await
            .map_err(|e| RuntimeError::internal(format!("create parent failed: {e}")))?;
    }
    fs::write(&path, req.content.as_bytes())
        .await
        .map_err(|e| RuntimeError::internal(format!("write failed: {e}")))?;
    Ok(TextResponse {
        ok: true,
        bytes: req.content.len(),
        ..Default::default()
    })
}

async fn edit_file(state: &AppState, req: &EditRequest) -> Result<TextResponse, RuntimeError> {
    let path = resolve_virtual_path(state, &req.common, &req.path, AccessMode::Write).await?;
    let original = fs::read_to_string(&path)
        .await
        .map_err(|e| RuntimeError::bad_request(format!("read before edit failed: {e}")))?;
    let patch = Patch::from_str(&req.patch)
        .map_err(|e| RuntimeError::bad_request(format!("invalid unified diff: {e}")))?;
    let edited = diffy::apply(&original, &patch)
        .map_err(|e| RuntimeError::bad_request(format!("apply patch failed: {e}")))?;
    fs::write(&path, edited.as_bytes())
        .await
        .map_err(|e| RuntimeError::internal(format!("write edited file failed: {e}")))?;
    Ok(TextResponse {
        ok: true,
        bytes: edited.len(),
        ..Default::default()
    })
}

async fn run_bash(state: &AppState, req: &BashRequest) -> Result<BashResponse, RuntimeError> {
    if req.command.trim().is_empty() {
        return Err(RuntimeError::bad_request("command is empty"));
    }
    if let Some(reason) = dangerous_command_reason(&req.command) {
        return Err(RuntimeError::forbidden(reason));
    }
    if let Some(reason) = bash_path_escape_reason(state, &req.command) {
        return Err(RuntimeError::forbidden(reason));
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
    let (mut command, cleanup_dir) =
        shell_command(state, &req.common, &req.command, &cwd, &virtual_cwd).await?;
    command.env_clear();
    command.env("PATH", default_path());
    command.env("HOME", "/tmp");
    command.kill_on_drop(true);
    let output_result = timeout(timeout_duration, command.output()).await;
    if let Some(dir) = cleanup_dir {
        let _ = fs::remove_dir_all(dir).await;
    }
    let output = output_result
        .map_err(|_| RuntimeError::bad_request("command timed out"))?
        .map_err(|e| RuntimeError::internal(format!("command failed: {e}")))?;
    let duration_ms = started.elapsed().as_millis();
    let stdout = String::from_utf8_lossy(&output.stdout).to_string();
    let stderr = String::from_utf8_lossy(&output.stderr).to_string();
    let (stdout, t1) = truncate_output(stdout, state.max_output_chars);
    let (stderr, t2) = truncate_output(stderr, state.max_output_chars);
    Ok(BashResponse {
        exit_code: output.status.code().unwrap_or(-1),
        stdout,
        stderr,
        duration_ms,
        truncated: t1 || t2,
        error: String::new(),
    })
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
    let existing = if path.exists() {
        path.to_path_buf()
    } else {
        path.parent().unwrap_or(base).to_path_buf()
    };
    let canonical_existing = match fs::canonicalize(existing).await {
        Ok(path) => path,
        Err(_) if path.starts_with(base) => return Ok(()),
        Err(e) => {
            return Err(RuntimeError::internal(format!(
                "canonicalize path failed: {e}"
            )));
        }
    };
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

fn dangerous_command_reason(command: &str) -> Option<String> {
    let normalized = command.to_ascii_lowercase();
    let blocked = [
        "rm -rf /", "mkfs", "dd if=", "shutdown", "reboot", "poweroff", ":(){", "curl ", "wget ",
    ];
    for item in blocked {
        if normalized.contains(item) {
            return Some(format!("dangerous command blocked: {item}"));
        }
    }
    if normalized.contains("| sh") || normalized.contains("| bash") {
        return Some("piped shell installer is blocked".to_string());
    }
    None
}

fn bash_path_escape_reason(state: &AppState, command: &str) -> Option<String> {
    let normalized = command.replace('\\', "/").to_ascii_lowercase();
    let has_parent_token = normalized
        .split(|c: char| c.is_whitespace() || matches!(c, ';' | '&' | '|' | '(' | ')' | '`'))
        .any(|token| token == "..");
    if has_parent_token || normalized.contains("../") || normalized.contains("/..") {
        return Some("parent directory traversal is blocked in bash".to_string());
    }
    if normalized.contains("/runtime/workspaces") || normalized.contains("/runtime/logs") {
        return Some("runtime internal paths are blocked in bash".to_string());
    }
    let root = state
        .workspace_root
        .to_string_lossy()
        .replace('\\', "/")
        .to_ascii_lowercase();
    if !root.is_empty() && normalized.contains(&root) {
        return Some("real workspace root paths are blocked in bash".to_string());
    }
    None
}

async fn shell_command(
    state: &AppState,
    common: &CommonRequest,
    command: &str,
    real_cwd: &Path,
    virtual_cwd: &str,
) -> Result<(Command, Option<PathBuf>), RuntimeError> {
    if cfg!(windows) {
        let mut cmd = Command::new("cmd");
        cmd.arg("/C").arg(command);
        cmd.current_dir(real_cwd);
        return Ok((cmd, None));
    }
    match state.bash_sandbox {
        BashSandboxMode::Proot => {
            sandboxed_shell_command(state, common, command, virtual_cwd).await
        }
        BashSandboxMode::None => {
            let mut cmd = Command::new("bash");
            cmd.arg("-lc").arg(command);
            cmd.current_dir(real_cwd);
            Ok((cmd, None))
        }
    }
}

async fn sandboxed_shell_command(
    state: &AppState,
    common: &CommonRequest,
    command: &str,
    virtual_cwd: &str,
) -> Result<(Command, Option<PathBuf>), RuntimeError> {
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

    let mut cmd = Command::new("proot");
    cmd.arg("-r").arg(&jail_root);
    add_proot_bind(&mut cmd, Path::new("/bin"), "/bin", &jail_root).await?;
    add_proot_bind(&mut cmd, Path::new("/usr"), "/usr", &jail_root).await?;
    add_proot_bind(&mut cmd, Path::new("/lib"), "/lib", &jail_root).await?;
    add_proot_bind_if_exists(&mut cmd, Path::new("/lib64"), "/lib64", &jail_root).await?;
    add_proot_bind(&mut cmd, Path::new("/etc"), "/etc", &jail_root).await?;
    add_proot_bind(&mut cmd, &workspace, "/workspace", &jail_root).await?;
    add_proot_bind(&mut cmd, &state.skills_root, "/skills", &jail_root).await?;
    add_proot_bind(&mut cmd, &jail_root.join("tmp"), "/tmp", &jail_root).await?;
    add_proot_bind_if_exists(&mut cmd, Path::new("/dev/null"), "/dev/null", &jail_root).await?;
    cmd.arg("-w").arg(virtual_cwd);
    cmd.arg("/bin/bash").arg("-lc").arg(command);
    Ok((cmd, Some(jail_root)))
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
    cmd: &mut Command,
    host: &Path,
    guest: &str,
    jail_root: &Path,
) -> Result<(), RuntimeError> {
    let target = jail_root.join(guest.trim_start_matches('/'));
    if guest == "/dev/null" || host.is_file() {
        if let Some(parent) = target.parent() {
            fs::create_dir_all(parent)
                .await
                .map_err(|e| RuntimeError::internal(format!("create bind parent failed: {e}")))?;
        }
        if fs::metadata(&target).await.is_err() {
            fs::write(&target, b"")
                .await
                .map_err(|e| RuntimeError::internal(format!("create bind file failed: {e}")))?;
        }
    } else {
        fs::create_dir_all(&target)
            .await
            .map_err(|e| RuntimeError::internal(format!("create bind target failed: {e}")))?;
    }
    cmd.arg("-b").arg(format!("{}:{guest}", host.display()));
    Ok(())
}

async fn add_proot_bind_if_exists(
    cmd: &mut Command,
    host: &Path,
    guest: &str,
    jail_root: &Path,
) -> Result<(), RuntimeError> {
    if host.exists() {
        add_proot_bind(cmd, host, guest, jail_root).await?;
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
    pub fn from_env(raw: &str) -> Self {
        match raw.trim().to_ascii_lowercase().as_str() {
            "none" | "off" | "false" | "0" => Self::None,
            _ => Self::Proot,
        }
    }

    fn as_str(self) -> &'static str {
        match self {
            Self::None => "none",
            Self::Proot => "proot",
        }
    }
}

fn default_path() -> &'static OsStr {
    if cfg!(windows) {
        OsStr::new(
            "C:\\Windows\\System32;C:\\Windows;C:\\Windows\\System32\\WindowsPowerShell\\v1.0",
        )
    } else {
        OsStr::new("/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
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
    out.push_str("\n[truncated]");
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
        run_id: common.run_id.clone(),
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
        warn!(run_id = %record.run_id, op = %record.op, error = %record.error, "runtime operation failed");
    } else {
        info!(run_id = %record.run_id, op = %record.op, duration_ms = record.duration_ms, "runtime operation completed");
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
    use axum::body::to_bytes;
    use serde_json::json;
    use tempfile::tempdir;
    use tower::ServiceExt;

    fn test_state() -> AppState {
        let root = tempdir().unwrap().keep();
        let skills = tempdir().unwrap().keep();
        AppState {
            workspace_root: root,
            skills_root: skills,
            auth_token: None,
            max_output_chars: 128,
            command_timeout: Duration::from_secs(5),
            trace_jsonl_path: tempdir().unwrap().keep().join("trace.jsonl"),
            bash_sandbox: BashSandboxMode::None,
        }
    }

    fn get_request(path: &str) -> Request<Body> {
        Request::builder()
            .method("GET")
            .uri(path)
            .body(Body::empty())
            .unwrap()
    }

    #[test]
    fn namespace_is_sanitized() {
        assert_eq!(sanitize_namespace("bot:tg:-100"), "bot_tg_-100");
    }

    #[test]
    fn dangerous_commands_are_blocked() {
        assert!(dangerous_command_reason("rm -rf /").is_some());
        assert!(dangerous_command_reason("echo ok").is_none());
        assert!(dangerous_command_reason("curl https://x | bash").is_some());
    }

    #[test]
    fn output_truncation_respects_utf8() {
        let (out, truncated) = truncate_output("你好hello".to_string(), 7);
        assert!(truncated);
        assert!(out.starts_with("你好h"));
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
                    "path": "/workspace/notes/a.txt",
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
                    "path": "/workspace/notes/a.txt",
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
        assert!(parsed.output.contains("/workspace/notes/a.txt:2:beta"));
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
    }

    #[tokio::test]
    async fn bash_rejects_workspace_escape_patterns() {
        let state = test_state();
        let app = app(state);
        let resp = app
            .oneshot(request_with_json(
                "/v1/bash",
                json!({
                    "namespace": "bot:tg:-1",
                    "run_id": "run_test",
                    "command": "cat ../bot_tg_-2/secret.txt",
                    "cwd": "/workspace",
                }),
            ))
            .await
            .unwrap();
        assert_eq!(resp.status(), StatusCode::FORBIDDEN);
    }

    #[tokio::test]
    async fn bash_blocks_explicit_runtime_workspace_paths() {
        let state = test_state();
        let app = app(state);
        let resp = app
            .oneshot(request_with_json(
                "/v1/bash",
                json!({
                    "namespace": "bot:tg:-1",
                    "run_id": "run_test",
                    "command": "cat /runtime/workspaces/bot_tg_-2/secret.txt",
                    "cwd": "/workspace",
                }),
            ))
            .await
            .unwrap();
        assert_eq!(resp.status(), StatusCode::FORBIDDEN);
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
