use super::super::{RuntimeFetchProxyError, output_error};
use cap_fs_ext::DirExt as _;
use cap_std::{ambient_authority, fs::Dir};
use std::{
    ffi::OsString,
    io,
    path::{Component, Path, PathBuf},
};

pub(in crate::runtime_fetch_proxy) fn validate_workspace_output_path(
    path: &str,
) -> Result<PathBuf, RuntimeFetchProxyError> {
    if path.contains('\\') {
        return Err(RuntimeFetchProxyError::policy(
            "output path must use a literal /workspace/ absolute path",
        ));
    }
    let Some(relative) = path.strip_prefix("/workspace/") else {
        return Err(RuntimeFetchProxyError::policy(
            "output path must be an absolute path below /workspace",
        ));
    };
    if relative.is_empty() {
        return Err(RuntimeFetchProxyError::policy(
            "output path must name a file",
        ));
    }
    let relative = PathBuf::from(relative);
    if relative
        .components()
        .any(|component| !matches!(component, Component::Normal(_)))
    {
        return Err(RuntimeFetchProxyError::policy(
            "output path contains a forbidden component",
        ));
    }
    Ok(relative)
}

pub(super) fn open_output_parent_nofollow(
    workspace_root: &Path,
    namespace: &str,
    relative: &Path,
) -> Result<(Dir, OsString), RuntimeFetchProxyError> {
    let root = Dir::open_ambient_dir(workspace_root, ambient_authority()).map_err(output_error)?;
    let mut directory = open_or_create_output_dir(&root, Path::new(namespace))?;
    let mut components = relative.components();
    let name = components
        .next_back()
        .ok_or_else(|| RuntimeFetchProxyError::policy("output path must name a file"))?;
    for component in components {
        let Component::Normal(name) = component else {
            return Err(RuntimeFetchProxyError::policy(
                "invalid output path component",
            ));
        };
        directory = open_or_create_output_dir(&directory, Path::new(name))?;
    }
    let Component::Normal(name) = name else {
        return Err(RuntimeFetchProxyError::policy("invalid output file name"));
    };
    Ok((directory, name.to_os_string()))
}

fn open_or_create_output_dir(parent: &Dir, name: &Path) -> Result<Dir, RuntimeFetchProxyError> {
    match parent.open_dir_nofollow(name) {
        Ok(directory) => Ok(directory),
        Err(error) if error.kind() == io::ErrorKind::NotFound => {
            if let Err(error) = parent.create_dir(name)
                && error.kind() != io::ErrorKind::AlreadyExists
            {
                return Err(output_error(error));
            }
            parent
                .open_dir_nofollow(name)
                .map_err(classify_output_parent_error)
        }
        Err(error) => Err(classify_output_parent_error(error)),
    }
}

fn classify_output_parent_error(error: io::Error) -> RuntimeFetchProxyError {
    if error.kind() == io::ErrorKind::NotADirectory || is_symlink_loop(&error) {
        RuntimeFetchProxyError::policy("output parent is not a safe directory")
    } else {
        output_error(error)
    }
}

#[cfg(unix)]
fn is_symlink_loop(error: &io::Error) -> bool {
    error.raw_os_error() == Some(libc::ELOOP)
}

#[cfg(not(unix))]
fn is_symlink_loop(_error: &io::Error) -> bool {
    false
}

pub(super) fn reject_cap_destination(
    parent: &Dir,
    destination: &OsString,
) -> Result<(), RuntimeFetchProxyError> {
    match parent.symlink_metadata(Path::new(destination)) {
        Ok(metadata) if metadata.file_type().is_symlink() || !metadata.is_file() => Err(
            RuntimeFetchProxyError::policy("output destination is not a safe regular file"),
        ),
        Ok(_) => Ok(()),
        Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(()),
        Err(error) => Err(output_error(error)),
    }
}

pub(super) fn unique_temporary_name(
    destination: &OsString,
) -> Result<OsString, RuntimeFetchProxyError> {
    let mut nonce = [0_u8; 8];
    getrandom::fill(&mut nonce)
        .map_err(|_| RuntimeFetchProxyError::new("create output temporary name failed"))?;
    let suffix = nonce
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect::<String>();
    Ok(OsString::from(format!(
        ".{}.agent-runtime-{suffix}.tmp",
        destination.to_string_lossy()
    )))
}

pub(super) fn sanitize_namespace(namespace: &str) -> String {
    namespace
        .chars()
        .map(|character| {
            if character.is_ascii_alphanumeric() || matches!(character, '-' | '_' | '.') {
                character
            } else {
                '_'
            }
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::runtime_fetch_proxy::RuntimeFetchProxyErrorCategory;

    #[test]
    fn unsafe_parent_types_are_policy_but_permission_and_io_are_internal() {
        assert_eq!(
            classify_output_parent_error(io::Error::from(io::ErrorKind::NotADirectory)).category(),
            RuntimeFetchProxyErrorCategory::Policy
        );
        assert_eq!(
            classify_output_parent_error(io::Error::from(io::ErrorKind::PermissionDenied))
                .category(),
            RuntimeFetchProxyErrorCategory::Internal
        );
        assert_eq!(
            classify_output_parent_error(io::Error::from(io::ErrorKind::ReadOnlyFilesystem))
                .category(),
            RuntimeFetchProxyErrorCategory::Internal
        );
    }
}
