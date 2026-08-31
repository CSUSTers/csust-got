use super::{
    MAX_SKILL_CONTENT_BYTES, MAX_SKILL_FILE_BYTES, MAX_SKILLS_PER_SOURCE, SkillDescriptor,
    SkillSnapshotError, is_canonical_skill_name, skill_description,
};
use cap_fs_ext::{DirExt as _, FollowSymlinks, OpenOptionsFollowExt as _};
use cap_std::{
    ambient_authority,
    fs::{Dir, File as CapFile, OpenOptions},
};
use sha2::{Digest, Sha256};
use std::{
    fs::Metadata,
    io::{self, Read as _},
    path::Path,
};

#[cfg(test)]
macro_rules! pre_open_hook {
    ($hook:ident, $phase:expr, $path:expr) => {
        || $hook($phase, $path)
    };
}

#[cfg(not(test))]
macro_rules! pre_open_hook {
    ($hook:ident, $phase:expr, $path:expr) => {
        || {}
    };
}

#[cfg(unix)]
type ObjectIdentity = (u64, u64);
#[cfg(windows)]
type ObjectIdentity = (u32, u64);
#[cfg(not(any(unix, windows)))]
type ObjectIdentity = ();

pub(super) fn load_runtime_skill_descriptors(
    root: &Path,
) -> Result<Vec<SkillDescriptor>, SkillSnapshotError> {
    #[cfg(test)]
    {
        load_runtime_skill_descriptors_with_hook(root, |_, _| {})
    }
    #[cfg(not(test))]
    {
        load_runtime_skill_descriptors_inner(root)
    }
}

#[cfg(test)]
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) enum RuntimeSkillLoadHookPoint {
    RootIdentity,
    ChildBoundary,
    SkillHandleOpened,
}

#[cfg(test)]
pub(super) fn load_runtime_skill_descriptors_with_hook(
    root: &Path,
    mut hook: impl FnMut(RuntimeSkillLoadHookPoint, &Path),
) -> Result<Vec<SkillDescriptor>, SkillSnapshotError> {
    load_runtime_skill_descriptors_inner(root, &mut hook)
}

fn load_runtime_skill_descriptors_inner(
    root: &Path,
    #[cfg(test)] hook: &mut dyn FnMut(RuntimeSkillLoadHookPoint, &Path),
) -> Result<Vec<SkillDescriptor>, SkillSnapshotError> {
    let root_dir = open_stable_handle(
        || open_root_nofollow(root),
        dir_identity,
        pre_open_hook!(hook, RuntimeSkillLoadHookPoint::RootIdentity, root),
        root,
        "skills root",
    )?;

    let mut descriptors = Vec::new();
    let mut total_content_bytes = 0usize;
    let entries = root_dir
        .entries()
        .map_err(|error| SkillSnapshotError::new(format!("read skills root: {error}")))?;
    for entry in entries {
        let entry = entry
            .map_err(|error| SkillSnapshotError::new(format!("read skills root entry: {error}")))?;
        let file_name = entry.file_name();
        let path = root.join(&file_name);
        let child_metadata = root_dir
            .symlink_metadata(Path::new(&file_name))
            .map_err(|error| {
                SkillSnapshotError::new(format!("inspect skill entry {}: {error}", path.display()))
            })?;
        if child_metadata.file_type().is_symlink() {
            return Err(entry_error(&path, "must not be a symlink"));
        }
        if child_metadata.is_file() {
            continue;
        }
        if !child_metadata.is_dir() {
            return Err(entry_error(&path, "must be a directory"));
        }

        let name = file_name
            .into_string()
            .map_err(|_| SkillSnapshotError::new("skill directory name must be valid UTF-8"))?;
        if !is_canonical_skill_name(&name) {
            return Err(SkillSnapshotError::new(format!(
                "skill directory {name:?} is not canonical"
            )));
        }
        if descriptors.len() == MAX_SKILLS_PER_SOURCE {
            return Err(capacity("skills root exceeds skill count capacity"));
        }

        let child_dir = open_stable_handle(
            || root_dir.open_dir_nofollow(Path::new(&name)),
            dir_identity,
            pre_open_hook!(hook, RuntimeSkillLoadHookPoint::ChildBoundary, &path),
            &path,
            "skill entry",
        )?;

        let skill_path = path.join("SKILL.md");
        #[cfg(test)]
        let skill_phase = RuntimeSkillLoadHookPoint::SkillHandleOpened;
        let skill_file = open_stable_handle(
            || open_skill_file_nofollow(&child_dir),
            file_identity,
            pre_open_hook!(hook, skill_phase, &skill_path),
            &skill_path,
            "skill file",
        )?;

        let bytes = read_skill_file(skill_file, &skill_path)?;
        let content =
            String::from_utf8(bytes).map_err(|_| skill_error(&skill_path, "is not valid UTF-8"))?;
        if content.is_empty() {
            return Err(skill_error(&skill_path, "must not be empty"));
        }
        let description = skill_description(&content)
            .ok_or_else(|| skill_error(&skill_path, "must contain prose"))?;
        total_content_bytes = total_content_bytes
            .checked_add(content.len())
            .ok_or_else(|| capacity("skills root content size overflows capacity"))?;
        if total_content_bytes > MAX_SKILL_CONTENT_BYTES {
            return Err(capacity("skills root exceeds aggregate content capacity"));
        }
        descriptors.push(SkillDescriptor {
            name: name.clone(),
            description,
            sha256: format!("{:x}", Sha256::digest(content.as_bytes())),
            content,
            source: "runtime-global".to_string(),
            virtual_path: format!("/skills/{name}/SKILL.md"),
        });
    }
    Ok(descriptors)
}

fn open_stable_handle<T>(
    mut open: impl FnMut() -> io::Result<T>,
    identity: impl Fn(&T) -> io::Result<ObjectIdentity>,
    on_pre_open: impl FnOnce(),
    path: &Path,
    label: &str,
) -> Result<T, SkillSnapshotError> {
    let pre = open().map_err(|error| open_error(label, path, error))?;
    let pre_identity = identity(&pre).map_err(|error| open_error(label, path, error))?;
    drop(pre);
    on_pre_open();
    let opened = open().map_err(|error| open_error(label, path, error))?;
    let opened_identity = identity(&opened).map_err(|error| open_error(label, path, error))?;
    let post = open().map_err(|error| open_error(label, path, error))?;
    let post_identity = identity(&post).map_err(|error| open_error(label, path, error))?;
    drop(post);
    if pre_identity == opened_identity && opened_identity == post_identity {
        Ok(opened)
    } else {
        Err(SkillSnapshotError::new(format!(
            "{label} {} changed while opening",
            path.display()
        )))
    }
}

fn open_error(label: &str, path: &Path, error: io::Error) -> SkillSnapshotError {
    SkillSnapshotError::new(format!("{label} {}: {error}", path.display()))
}

fn skill_error(path: &Path, message: &str) -> SkillSnapshotError {
    SkillSnapshotError::new(format!("{} {message}", path.display()))
}

fn entry_error(path: &Path, message: &str) -> SkillSnapshotError {
    SkillSnapshotError::new(format!("skill entry {} {message}", path.display()))
}

fn capacity(message: &'static str) -> SkillSnapshotError {
    SkillSnapshotError::new(message)
}

fn open_root_nofollow(root: &Path) -> io::Result<Dir> {
    let name = root.file_name().ok_or_else(|| {
        io::Error::new(
            io::ErrorKind::InvalidInput,
            "skills root must name a non-root directory",
        )
    })?;
    let parent = root
        .parent()
        .filter(|parent| !parent.as_os_str().is_empty())
        .unwrap_or(Path::new("."));
    Dir::open_ambient_dir(parent, ambient_authority())
        .and_then(|parent| parent.open_dir_nofollow(Path::new(name)))
}

fn open_skill_file_nofollow(parent: &Dir) -> io::Result<CapFile> {
    let mut options = OpenOptions::new();
    options.read(true).follow(FollowSymlinks::No);
    parent.open_with(Path::new("SKILL.md"), &options)
}

fn dir_identity(directory: &Dir) -> io::Result<ObjectIdentity> {
    handle_identity(&directory.try_clone()?.into_std_file(), Metadata::is_dir)
}

fn file_identity(file: &CapFile) -> io::Result<ObjectIdentity> {
    handle_identity(&file.try_clone()?.into_std(), Metadata::is_file)
}

fn handle_identity(
    file: &std::fs::File,
    expected: fn(&Metadata) -> bool,
) -> io::Result<ObjectIdentity> {
    let metadata = file.metadata()?;
    if metadata.file_type().is_symlink() || !expected(&metadata) {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "opened handle has an unexpected type",
        ));
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::MetadataExt as _;

        Ok((metadata.dev(), metadata.ino()))
    }
    #[cfg(windows)]
    {
        windows_handle_identity(file)
    }
    #[cfg(not(any(unix, windows)))]
    {
        Err(io::Error::new(
            io::ErrorKind::Unsupported,
            "exact handle identity is unsupported",
        ))
    }
}

fn read_skill_file(file: CapFile, path: &Path) -> Result<Vec<u8>, SkillSnapshotError> {
    let mut bytes = Vec::with_capacity(MAX_SKILL_FILE_BYTES.min(4096));
    file.take((MAX_SKILL_FILE_BYTES + 1) as u64)
        .read_to_end(&mut bytes)
        .map_err(|error| SkillSnapshotError::new(format!("read {}: {error}", path.display())))?;
    if bytes.len() > MAX_SKILL_FILE_BYTES {
        return Err(skill_error(path, "exceeds per-file capacity"));
    }
    Ok(bytes)
}

#[cfg(windows)]
#[repr(C)]
#[derive(Default)]
struct FileTime {
    low_date_time: u32,
    high_date_time: u32,
}

#[cfg(windows)]
#[repr(C)]
#[derive(Default)]
struct ByHandleFileInformation {
    file_attributes: u32,
    creation_time: FileTime,
    last_access_time: FileTime,
    last_write_time: FileTime,
    volume_serial_number: u32,
    file_size_high: u32,
    file_size_low: u32,
    number_of_links: u32,
    file_index_high: u32,
    file_index_low: u32,
}

#[cfg(windows)]
#[link(name = "kernel32")]
unsafe extern "system" {
    fn GetFileInformationByHandle(
        handle: *mut core::ffi::c_void,
        information: *mut ByHandleFileInformation,
    ) -> i32;
}

#[cfg(windows)]
fn windows_handle_identity(file: &std::fs::File) -> io::Result<ObjectIdentity> {
    use std::os::windows::io::AsRawHandle as _;

    let mut information = ByHandleFileInformation::default();
    // SAFETY: `file` supplies a live handle and `information` has the Win32-compatible writable layout.
    if unsafe { GetFileInformationByHandle(file.as_raw_handle(), &mut information) } == 0 {
        return Err(io::Error::last_os_error());
    }
    Ok((
        information.volume_serial_number,
        (u64::from(information.file_index_high) << 32) | u64::from(information.file_index_low),
    ))
}
