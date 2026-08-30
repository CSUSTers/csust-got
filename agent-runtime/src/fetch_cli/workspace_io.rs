use super::{FetchError, FetchErrorKind, MAX_REQUEST_BODY_BYTES};
use cap_fs_ext::{DirExt as _, FollowSymlinks, OpenOptionsFollowExt as _};
use cap_std::{
    ambient_authority,
    fs::{Dir, File, OpenOptions},
};
use std::{
    ffi::OsString,
    path::{Component, Path, PathBuf},
};

const WORKSPACE_ROOT: &str = "/workspace";

pub(crate) struct InputFile {
    pub file: File,
    pub len: u64,
    pub name: String,
}

pub(crate) fn open_input(path: &Path) -> Result<InputFile, FetchError> {
    let resolved = resolve_workspace_input(path)?;
    let (parent, name, _) = open_parent(&resolved)?;
    let metadata = parent
        .symlink_metadata(Path::new(&name))
        .map_err(|error| policy(format!("input path cannot be inspected safely: {error}")))?;
    if metadata.file_type().is_symlink() || !metadata.file_type().is_file() {
        return Err(policy("input path must be a non-symlink regular file"));
    }
    if metadata.len() > MAX_REQUEST_BODY_BYTES as u64 {
        return Err(policy(format!(
            "request body exceeds {} byte limit",
            MAX_REQUEST_BODY_BYTES
        )));
    }
    let mut options = OpenOptions::new();
    options.read(true).follow(FollowSymlinks::No);
    let file = parent
        .open_with(Path::new(&name), &options)
        .map_err(|error| policy(format!("input path cannot be opened safely: {error}")))?;
    Ok(InputFile {
        file,
        len: metadata.len(),
        name: name.to_string_lossy().into_owned(),
    })
}

pub(crate) fn normalize_output_path(path: &Path) -> Result<String, FetchError> {
    resolve_workspace_path(path)?
        .to_str()
        .map(str::to_string)
        .ok_or_else(|| policy("output path must be valid UTF-8"))
}

fn resolve_workspace_input(path: &Path) -> Result<PathBuf, FetchError> {
    resolve_workspace_path(path)
}

fn resolve_workspace_path(path: &Path) -> Result<PathBuf, FetchError> {
    if path.as_os_str().is_empty() {
        return Err(policy("workspace path is empty"));
    }
    let workspace = Path::new(WORKSPACE_ROOT);
    let relative = if path.is_absolute() {
        path.strip_prefix(workspace)
            .map_err(|_| policy("absolute paths must be under /workspace"))?
    } else {
        path
    };
    let mut candidate = workspace.to_path_buf();
    let mut has_file_name = false;
    for component in relative.components() {
        match component {
            Component::Normal(name) => {
                candidate.push(name);
                has_file_name = true;
            }
            Component::CurDir => {}
            _ => return Err(policy("path traversal is not allowed")),
        }
    }
    if !has_file_name {
        return Err(policy("path must name a file"));
    }
    Ok(candidate)
}

fn open_parent(path: &Path) -> Result<(Dir, OsString, PathBuf), FetchError> {
    let root = PathBuf::from(WORKSPACE_ROOT);
    let relative = path
        .strip_prefix(&root)
        .map_err(|_| policy("path escapes the workspace"))?;
    let mut components = relative
        .components()
        .filter_map(|component| match component {
            Component::Normal(name) => Some(name.to_os_string()),
            _ => None,
        })
        .collect::<Vec<_>>();
    let name = components
        .pop()
        .ok_or_else(|| policy("path must name a file"))?;
    let mut directory = Dir::open_ambient_dir(&root, ambient_authority())
        .map_err(|error| policy(format!("workspace cannot be opened safely: {error}")))?;
    let mut parent_path = root;
    for component in components {
        directory = directory
            .open_dir_nofollow(Path::new(&component))
            .map_err(|error| policy(format!("path parent cannot be opened safely: {error}")))?;
        parent_path.push(component);
    }
    Ok((directory, name, parent_path))
}

fn policy(message: impl Into<String>) -> FetchError {
    FetchError::new(FetchErrorKind::Policy, message)
}
