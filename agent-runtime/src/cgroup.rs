use sha2::{Digest, Sha256};
use std::{
    collections::BTreeSet,
    fmt, fs, io,
    path::{Path, PathBuf},
    time::{Duration, Instant},
};

const COMMAND_PREFIX: &str = "cmd-";
const HASH_COMPONENT_LEN: usize = 20;
const REQUIRED_CONTROLLERS: [&str; 3] = ["pids", "memory", "cpu"];

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct CommandLimits {
    pub pids_max: u64,
    pub memory_max_bytes: u64,
    pub memory_swap_max_bytes: u64,
    pub cpu_quota_us: u64,
    pub cpu_period_us: u64,
    pub cpu_budget: Duration,
    pub cleanup_timeout: Duration,
}

impl CommandLimits {
    pub fn approved_defaults() -> Self {
        Self {
            pids_max: 64,
            memory_max_bytes: 256 * 1024 * 1024,
            memory_swap_max_bytes: 0,
            cpu_quota_us: 100_000,
            cpu_period_us: 100_000,
            cpu_budget: Duration::from_secs(120),
            cleanup_timeout: Duration::from_secs(10),
        }
    }

    pub fn with_cpu_budget(mut self, effective_wall_timeout: Duration) -> Self {
        self.cpu_budget = effective_wall_timeout;
        self
    }

    fn validate(&self) -> Result<(), CgroupError> {
        if self.pids_max == 0
            || self.memory_max_bytes == 0
            || self.cpu_quota_us == 0
            || self.cpu_period_us == 0
            || self.cpu_budget.is_zero()
            || self.cleanup_timeout.is_zero()
        {
            return Err(CgroupError::new(
                "cgroup limits must be finite and positive except memory.swap.max",
            ));
        }
        Ok(())
    }
}

#[derive(Clone, Debug)]
pub struct CgroupConfig {
    pub root: PathBuf,
    pub limits: CommandLimits,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct CgroupTopologyConfig {
    pub aggregate_root: PathBuf,
    pub commands_root: PathBuf,
}

impl CgroupTopologyConfig {
    pub fn validate_runtime(&self) -> Result<PathBuf, CgroupError> {
        let membership = fs::read_to_string("/proc/self/cgroup").map_err(|error| {
            CgroupError::io(
                "read runtime cgroup membership",
                Path::new("/proc/self/cgroup"),
                error,
            )
        })?;
        self.validate_runtime_from(&membership, Path::new("/sys/fs/cgroup"))
    }

    pub fn validate_runtime_from(
        &self,
        membership: &str,
        cgroup_mount: &Path,
    ) -> Result<PathBuf, CgroupError> {
        let unified = parse_unified_cgroup_path(membership)?;
        let relative = unified
            .strip_prefix(Path::new("/"))
            .map_err(|_| CgroupError::new("runtime unified cgroup path must be absolute"))?;
        let runtime_cgroup = cgroup_mount.join(relative);
        validate_runtime_topology(&self.aggregate_root, &self.commands_root, &runtime_cgroup)?;
        canonical_directory(&runtime_cgroup, "runtime cgroup")
    }
}

impl CgroupConfig {
    pub fn validate_aggregate_relationship(
        &self,
        aggregate_parent: impl AsRef<Path>,
        runtime_cgroup: impl AsRef<Path>,
    ) -> Result<(), CgroupError> {
        let aggregate_parent = canonical_directory(aggregate_parent.as_ref(), "aggregate parent")?;
        let command_root = canonical_directory(&self.root, "command cgroup root")?;
        let runtime_cgroup = canonical_directory(runtime_cgroup.as_ref(), "runtime cgroup")?;
        validate_canonical_runtime_topology(&aggregate_parent, &command_root, &runtime_cgroup)
    }
}

pub fn parse_unified_cgroup_path(contents: &str) -> Result<PathBuf, CgroupError> {
    let lines = contents.lines().collect::<Vec<_>>();
    if lines.len() != 1 {
        return Err(CgroupError::new(
            "runtime must have exactly one unified cgroup v2 membership",
        ));
    }
    let Some(path) = lines[0].strip_prefix("0::") else {
        return Err(CgroupError::new(
            "runtime cgroup membership must be a unified 0:: entry",
        ));
    };
    if !path.starts_with('/')
        || path
            .split('/')
            .any(|component| matches!(component, "." | ".."))
    {
        return Err(CgroupError::new(
            "runtime unified cgroup path must be absolute without parent components",
        ));
    }
    Ok(PathBuf::from(path))
}

fn validate_runtime_topology(
    aggregate_parent: &Path,
    command_root: &Path,
    runtime_cgroup: &Path,
) -> Result<(), CgroupError> {
    let aggregate_parent = canonical_directory(aggregate_parent, "aggregate parent")?;
    let command_root = canonical_directory(command_root, "command cgroup root")?;
    let runtime_cgroup = canonical_directory(runtime_cgroup, "runtime cgroup")?;
    validate_canonical_runtime_topology(&aggregate_parent, &command_root, &runtime_cgroup)
}

fn validate_canonical_runtime_topology(
    aggregate_parent: &Path,
    command_root: &Path,
    runtime_cgroup: &Path,
) -> Result<(), CgroupError> {
    if aggregate_parent == command_root
        || aggregate_parent == runtime_cgroup
        || command_root == runtime_cgroup
        || command_root.parent() != Some(aggregate_parent)
        || runtime_cgroup.parent() != Some(aggregate_parent)
    {
        return Err(CgroupError::new(
            "runtime and command cgroups must be distinct direct children of the approved aggregate parent",
        ));
    }
    validate_controller_file(
        &aggregate_parent.join("cgroup.controllers"),
        "available in the aggregate parent",
    )?;
    validate_controller_file(
        &aggregate_parent.join("cgroup.subtree_control"),
        "enabled in the aggregate parent",
    )?;
    validate_aggregate_limits(aggregate_parent)
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct CommandIdentity {
    pub namespace_hash: String,
    pub run_id_hash: String,
    pub command_id: String,
}

impl CommandIdentity {
    pub fn new(namespace: &str, run_id: &str, command_id: &str) -> Self {
        Self {
            namespace_hash: hash_component(namespace),
            run_id_hash: hash_component(run_id),
            command_id: hash_component(command_id),
        }
    }

    pub fn cgroup_name(&self) -> String {
        format!(
            "{COMMAND_PREFIX}{}-{}-{}",
            self.namespace_hash, self.run_id_hash, self.command_id
        )
    }
}

#[derive(Debug)]
pub struct CgroupError {
    message: String,
}

impl CgroupError {
    fn new(message: impl Into<String>) -> Self {
        Self {
            message: message.into(),
        }
    }

    fn io(operation: &str, path: &Path, error: io::Error) -> Self {
        Self::new(format!("{operation} {}: {error}", path.display()))
    }
}

impl fmt::Display for CgroupError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(&self.message)
    }
}

impl std::error::Error for CgroupError {}

#[derive(Clone, Debug)]
pub struct CgroupManager {
    root: PathBuf,
    limits: CommandLimits,
    #[cfg(any(test, feature = "c7-test-support"))]
    fixture: bool,
    #[cfg(any(test, feature = "c7-test-support"))]
    fixture_kill_log: std::sync::Arc<std::sync::Mutex<Vec<PathBuf>>>,
    #[cfg(any(test, feature = "c7-test-support"))]
    fixture_fail_control: Option<&'static str>,
}

impl CgroupManager {
    pub fn validate(root: impl AsRef<Path>, limits: CommandLimits) -> Result<Self, CgroupError> {
        Self::validate_inner(root.as_ref(), limits, false)
    }

    fn validate_inner(
        root: &Path,
        limits: CommandLimits,
        #[allow(unused_variables)] fixture: bool,
    ) -> Result<Self, CgroupError> {
        limits.validate()?;
        let root = canonical_directory(root, "command cgroup root")?;
        validate_controller_file(&root.join("cgroup.controllers"), "available")?;
        validate_controller_file(&root.join("cgroup.subtree_control"), "enabled")?;
        open_writable(&root.join("cgroup.procs"))?;
        open_writable(&root.join("cgroup.subtree_control"))?;
        Ok(Self {
            root,
            limits,
            #[cfg(any(test, feature = "c7-test-support"))]
            fixture,
            #[cfg(any(test, feature = "c7-test-support"))]
            fixture_kill_log: std::sync::Arc::new(std::sync::Mutex::new(Vec::new())),
            #[cfg(any(test, feature = "c7-test-support"))]
            fixture_fail_control: None,
        })
    }

    #[cfg(any(test, feature = "c7-test-support"))]
    pub(crate) fn validate_fixture(
        root: impl AsRef<Path>,
        limits: CommandLimits,
    ) -> Result<Self, CgroupError> {
        Self::validate_inner(root.as_ref(), limits, true)
    }

    #[cfg(feature = "c7-test-support")]
    fn validate_fixture_with_control_failure(
        root: impl AsRef<Path>,
        limits: CommandLimits,
        control: &'static str,
    ) -> Result<Self, CgroupError> {
        let mut manager = Self::validate_inner(root.as_ref(), limits, true)?;
        manager.fixture_fail_control = Some(control);
        Ok(manager)
    }

    pub fn limits(&self) -> &CommandLimits {
        &self.limits
    }

    pub fn recover_stale(&self) -> Result<(), CgroupError> {
        let entries = fs::read_dir(&self.root)
            .map_err(|error| CgroupError::io("read cgroup root", &self.root, error))?;
        for entry in entries {
            let entry =
                entry.map_err(|error| CgroupError::io("read cgroup entry", &self.root, error))?;
            let file_type = entry
                .file_type()
                .map_err(|error| CgroupError::io("inspect cgroup entry", &entry.path(), error))?;
            if file_type.is_dir()
                && entry
                    .file_name()
                    .to_string_lossy()
                    .starts_with(COMMAND_PREFIX)
            {
                self.kill_wait_remove_blocking(entry.path())?;
            }
        }
        Ok(())
    }

    pub fn create(&self, id: &CommandIdentity) -> Result<CommandCgroup, CgroupError> {
        let name = id.cgroup_name();
        let path = self.root.join(&name);
        fs::create_dir(&path)
            .map_err(|error| CgroupError::io("create command cgroup", &path, error))?;
        let result = (|| {
            #[cfg(any(test, feature = "c7-test-support"))]
            if self.fixture {
                fs::write(path.join("cgroup.procs"), b"")
                    .and_then(|()| fs::write(path.join("cgroup.events"), b"populated 0\n"))
                    .and_then(|()| fs::write(path.join("cgroup.kill"), b""))
                    .map_err(|error| {
                        CgroupError::io("create fixture cgroup controls", &path, error)
                    })?;
            }
            self.write_limit_control(&path, "pids.max", self.limits.pids_max.to_string())?;
            self.write_limit_control(
                &path,
                "memory.max",
                self.limits.memory_max_bytes.to_string(),
            )?;
            self.write_limit_control(
                &path,
                "memory.swap.max",
                self.limits.memory_swap_max_bytes.to_string(),
            )?;
            self.write_limit_control(&path, "memory.oom.group", "1")?;
            self.write_limit_control(
                &path,
                "cpu.max",
                format!("{} {}", self.limits.cpu_quota_us, self.limits.cpu_period_us),
            )?;
            Ok(())
        })();
        if let Err(error) = result {
            if let Err(cleanup_error) = self.remove_created_group(&path) {
                return Err(CgroupError::new(format!(
                    "{error}; remove partially created command cgroup: {cleanup_error}"
                )));
            }
            return Err(error);
        }
        Ok(CommandCgroup {
            procs_path: path.join("cgroup.procs"),
            path,
            name,
            cleanup_timeout: self.limits.cleanup_timeout,
            #[cfg(any(test, feature = "c7-test-support"))]
            fixture: self.fixture,
            #[cfg(any(test, feature = "c7-test-support"))]
            fixture_kill_log: self.fixture_kill_log.clone(),
        })
    }

    fn write_limit_control(
        &self,
        path: &Path,
        name: &'static str,
        value: impl AsRef<[u8]>,
    ) -> Result<(), CgroupError> {
        #[cfg(any(test, feature = "c7-test-support"))]
        if self.fixture_fail_control == Some(name) {
            return Err(CgroupError::new(format!(
                "injected cgroup control write failure: {name}"
            )));
        }
        write_control(path, name, value)
    }

    fn kill_wait_remove_blocking(&self, path: PathBuf) -> Result<(), CgroupError> {
        #[cfg(any(test, feature = "c7-test-support"))]
        if self.fixture {
            write_existing_control(&path.join("cgroup.kill"), "1")?;
            self.fixture_kill_log.lock().unwrap().push(path.clone());
            wait_until_empty_blocking(&path, self.limits.cleanup_timeout)?;
            return self.remove_group(&path);
        }
        kill_wait_remove_path_blocking(&path, self.limits.cleanup_timeout)
    }

    fn remove_created_group(&self, path: &Path) -> Result<(), CgroupError> {
        #[cfg(any(test, feature = "c7-test-support"))]
        if self.fixture {
            return fs::remove_dir_all(path)
                .map_err(|error| CgroupError::io("remove command cgroup", path, error));
        }
        fs::remove_dir(path).map_err(|error| CgroupError::io("remove command cgroup", path, error))
    }

    #[cfg(any(test, feature = "c7-test-support"))]
    fn remove_group(&self, path: &Path) -> Result<(), CgroupError> {
        #[cfg(any(test, feature = "c7-test-support"))]
        if self.fixture {
            return fs::remove_dir_all(path)
                .map_err(|error| CgroupError::io("remove command cgroup", path, error));
        }
        fs::remove_dir(path).map_err(|error| CgroupError::io("remove command cgroup", path, error))
    }

    #[cfg(any(test, feature = "c7-test-support"))]
    pub(crate) fn fixture_kill_log(&self) -> Vec<PathBuf> {
        self.fixture_kill_log.lock().unwrap().clone()
    }
}

#[derive(Debug)]
pub struct CommandCgroup {
    path: PathBuf,
    procs_path: PathBuf,
    name: String,
    cleanup_timeout: Duration,
    #[cfg(any(test, feature = "c7-test-support"))]
    fixture: bool,
    #[cfg(any(test, feature = "c7-test-support"))]
    fixture_kill_log: std::sync::Arc<std::sync::Mutex<Vec<PathBuf>>>,
}

impl CommandCgroup {
    pub fn path(&self) -> &Path {
        &self.path
    }

    pub fn name(&self) -> &str {
        &self.name
    }

    pub fn procs_path(&self) -> &Path {
        &self.procs_path
    }

    pub fn cpu_usage(&self) -> Result<Duration, CgroupError> {
        let path = self.path.join("cpu.stat");
        let contents = fs::read_to_string(&path)
            .map_err(|error| CgroupError::io("read cpu.stat", &path, error))?;
        let usage = contents
            .lines()
            .find_map(|line| {
                let mut fields = line.split_whitespace();
                match (fields.next(), fields.next(), fields.next()) {
                    (Some("usage_usec"), Some(value), None) => value.parse::<u64>().ok(),
                    _ => None,
                }
            })
            .ok_or_else(|| CgroupError::new(format!("invalid cpu.stat at {}", path.display())))?;
        Ok(Duration::from_micros(usage))
    }

    pub async fn kill_wait_remove(self) -> Result<(), CgroupError> {
        self.kill_wait_remove_inner().await
    }

    pub fn kill_wait_remove_blocking(self) -> Result<(), CgroupError> {
        #[cfg(any(test, feature = "c7-test-support"))]
        if self.fixture {
            write_existing_control(&self.path.join("cgroup.kill"), "1")?;
            self.fixture_kill_log
                .lock()
                .unwrap()
                .push(self.path.clone());
            wait_until_empty_blocking(&self.path, self.cleanup_timeout)?;
            return fs::remove_dir_all(&self.path)
                .map_err(|error| CgroupError::io("remove command cgroup", &self.path, error));
        }
        kill_wait_remove_path_blocking(&self.path, self.cleanup_timeout)
    }

    async fn kill_wait_remove_inner(self) -> Result<(), CgroupError> {
        #[cfg(any(test, feature = "c7-test-support"))]
        if self.fixture {
            write_existing_control(&self.path.join("cgroup.kill"), "1")?;
            self.fixture_kill_log
                .lock()
                .unwrap()
                .push(self.path.clone());
            wait_until_empty_async(&self.path, self.cleanup_timeout).await?;
            return fs::remove_dir_all(&self.path)
                .map_err(|error| CgroupError::io("remove command cgroup", &self.path, error));
        }
        kill_wait_remove_path_async(&self.path, self.cleanup_timeout).await
    }
}

fn hash_component(value: &str) -> String {
    let digest = Sha256::digest(value.as_bytes());
    let encoded = format!("{digest:x}");
    encoded[..HASH_COMPONENT_LEN].to_string()
}

fn canonical_directory(path: &Path, description: &str) -> Result<PathBuf, CgroupError> {
    let canonical = fs::canonicalize(path)
        .map_err(|error| CgroupError::io(&format!("open {description}"), path, error))?;
    let metadata = fs::metadata(&canonical)
        .map_err(|error| CgroupError::io(&format!("inspect {description}"), &canonical, error))?;
    if !metadata.is_dir() {
        return Err(CgroupError::new(format!(
            "{description} {} is not a directory",
            canonical.display()
        )));
    }
    Ok(canonical)
}

fn validate_controller_file(path: &Path, state: &str) -> Result<(), CgroupError> {
    let contents = fs::read_to_string(path)
        .map_err(|error| CgroupError::io("read cgroup controller file", path, error))?;
    let controllers: BTreeSet<_> = contents
        .split_whitespace()
        .map(|controller| controller.trim_start_matches('+'))
        .collect();
    for required in REQUIRED_CONTROLLERS {
        if !controllers.contains(required) {
            return Err(CgroupError::new(format!(
                "required cgroup controller {required} is not {state} in {}",
                path.display()
            )));
        }
    }
    Ok(())
}

fn open_writable(path: &Path) -> Result<(), CgroupError> {
    fs::OpenOptions::new()
        .write(true)
        .open(path)
        .map(|_| ())
        .map_err(|error| CgroupError::io("open writable cgroup control", path, error))
}

fn write_control(directory: &Path, name: &str, value: impl AsRef<[u8]>) -> Result<(), CgroupError> {
    let path = directory.join(name);
    fs::write(&path, value).map_err(|error| CgroupError::io("write cgroup control", &path, error))
}

fn write_existing_control(path: &Path, value: &str) -> Result<(), CgroupError> {
    let mut file = fs::OpenOptions::new()
        .write(true)
        .truncate(true)
        .open(path)
        .map_err(|error| CgroupError::io("open cgroup control", path, error))?;
    use std::io::Write as _;
    file.write_all(value.as_bytes())
        .map_err(|error| CgroupError::io("write cgroup control", path, error))
}

fn cgroup_is_populated(path: &Path) -> Result<bool, CgroupError> {
    let events_path = path.join("cgroup.events");
    let contents = fs::read_to_string(&events_path)
        .map_err(|error| CgroupError::io("read cgroup.events", &events_path, error))?;
    contents
        .lines()
        .find_map(|line| {
            let mut fields = line.split_whitespace();
            match (fields.next(), fields.next(), fields.next()) {
                (Some("populated"), Some("0"), None) => Some(false),
                (Some("populated"), Some("1"), None) => Some(true),
                _ => None,
            }
        })
        .ok_or_else(|| {
            CgroupError::new(format!(
                "invalid cgroup.events at {}",
                events_path.display()
            ))
        })
}

fn wait_until_empty_blocking(path: &Path, cleanup_timeout: Duration) -> Result<(), CgroupError> {
    let deadline = Instant::now() + cleanup_timeout;
    loop {
        if !cgroup_is_populated(path)? {
            return Ok(());
        }
        if Instant::now() >= deadline {
            return Err(cleanup_timeout_error(path));
        }
        std::thread::sleep(Duration::from_millis(10));
    }
}

async fn wait_until_empty_async(path: &Path, cleanup_timeout: Duration) -> Result<(), CgroupError> {
    let deadline = tokio::time::Instant::now() + cleanup_timeout;
    loop {
        if !cgroup_is_populated(path)? {
            return Ok(());
        }
        if tokio::time::Instant::now() >= deadline {
            return Err(cleanup_timeout_error(path));
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
}

fn kill_wait_remove_path_blocking(
    path: &Path,
    cleanup_timeout: Duration,
) -> Result<(), CgroupError> {
    let deadline = Instant::now() + cleanup_timeout;
    loop {
        write_existing_control(&path.join("cgroup.kill"), "1")?;
        wait_until_empty_blocking(path, deadline.saturating_duration_since(Instant::now()))?;
        match fs::remove_dir(path) {
            Ok(()) => return Ok(()),
            Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(()),
            Err(error) if retryable_cgroup_remove(&error) && Instant::now() < deadline => {
                std::thread::sleep(Duration::from_millis(10));
            }
            Err(error) => {
                return Err(CgroupError::io("remove command cgroup", path, error));
            }
        }
    }
}

async fn kill_wait_remove_path_async(
    path: &Path,
    cleanup_timeout: Duration,
) -> Result<(), CgroupError> {
    let deadline = tokio::time::Instant::now() + cleanup_timeout;
    loop {
        write_existing_control(&path.join("cgroup.kill"), "1")?;
        wait_until_empty_async(
            path,
            deadline.saturating_duration_since(tokio::time::Instant::now()),
        )
        .await?;
        match fs::remove_dir(path) {
            Ok(()) => return Ok(()),
            Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(()),
            Err(error)
                if retryable_cgroup_remove(&error) && tokio::time::Instant::now() < deadline =>
            {
                tokio::time::sleep(Duration::from_millis(10)).await;
            }
            Err(error) => {
                return Err(CgroupError::io("remove command cgroup", path, error));
            }
        }
    }
}

fn retryable_cgroup_remove(error: &io::Error) -> bool {
    error.kind() == io::ErrorKind::DirectoryNotEmpty
        || matches!(error.raw_os_error(), Some(16 | 39))
}

fn cleanup_timeout_error(path: &Path) -> CgroupError {
    CgroupError::new(format!(
        "timed out waiting for cgroup {} to become empty",
        path.display()
    ))
}

fn validate_aggregate_limits(parent: &Path) -> Result<(), CgroupError> {
    for (file, approved) in [
        ("pids.max", "512"),
        ("memory.max", "1073741824"),
        ("memory.swap.max", "0"),
        ("cpu.max", "200000 100000"),
    ] {
        let path = parent.join(file);
        let actual = fs::read_to_string(&path)
            .map_err(|error| CgroupError::io("read aggregate cgroup limit", &path, error))?;
        if actual.trim() != approved {
            return Err(CgroupError::new(format!(
                "aggregate cgroup {} must be {approved}, got {}",
                path.display(),
                actual.trim()
            )));
        }
    }
    Ok(())
}

#[cfg(feature = "c7-test-support")]
pub mod c7_test_support;

#[cfg(test)]
mod tests {
    use super::{CgroupManager, CommandIdentity, CommandLimits};
    use std::{fs, time::Duration};
    use tempfile::tempdir;

    fn fake_root(controllers: &str) -> tempfile::TempDir {
        let root = tempdir().unwrap();
        fs::write(root.path().join("cgroup.controllers"), controllers).unwrap();
        fs::write(root.path().join("cgroup.subtree_control"), controllers).unwrap();
        fs::write(root.path().join("cgroup.procs"), "").unwrap();
        root
    }

    #[test]
    fn command_defaults_write_the_approved_limits() {
        let root = fake_root("pids memory cpu");
        let manager = CgroupManager::validate_fixture(
            root.path(),
            CommandLimits::approved_defaults().with_cpu_budget(Duration::from_secs(7)),
        )
        .unwrap();
        let group = manager
            .create(&CommandIdentity::new("namespace", "run", "command"))
            .unwrap();

        assert_eq!(
            fs::read_to_string(group.path().join("pids.max")).unwrap(),
            "64"
        );
        assert_eq!(
            fs::read_to_string(group.path().join("memory.max")).unwrap(),
            "268435456"
        );
        assert_eq!(
            fs::read_to_string(group.path().join("memory.swap.max")).unwrap(),
            "0"
        );
        assert_eq!(
            fs::read_to_string(group.path().join("memory.oom.group")).unwrap(),
            "1"
        );
        assert_eq!(
            fs::read_to_string(group.path().join("cpu.max")).unwrap(),
            "100000 100000"
        );
        assert_eq!(manager.limits().cpu_budget, Duration::from_secs(7));
    }

    #[test]
    fn validation_rejects_missing_controller_and_nonwritable_procs() {
        let missing = fake_root("pids memory");
        let error = CgroupManager::validate_fixture(
            missing.path(),
            CommandLimits::approved_defaults().with_cpu_budget(Duration::from_secs(1)),
        )
        .unwrap_err();
        assert!(error.to_string().contains("cpu"));

        let nonwritable = fake_root("pids memory cpu");
        fs::remove_file(nonwritable.path().join("cgroup.procs")).unwrap();
        fs::create_dir(nonwritable.path().join("cgroup.procs")).unwrap();
        let error = CgroupManager::validate_fixture(
            nonwritable.path(),
            CommandLimits::approved_defaults().with_cpu_budget(Duration::from_secs(1)),
        )
        .unwrap_err();
        assert!(error.to_string().contains("cgroup.procs"));
    }

    #[tokio::test]
    async fn stale_recovery_kills_waits_for_empty_and_removes_groups() {
        let root = fake_root("pids memory cpu");
        let stale = root.path().join("cmd-stale");
        fs::create_dir(&stale).unwrap();
        fs::write(stale.join("cgroup.kill"), "0").unwrap();
        fs::write(stale.join("cgroup.events"), "populated 0\n").unwrap();
        let expected_stale = fs::canonicalize(&stale).unwrap();
        let manager = CgroupManager::validate_fixture(
            root.path(),
            CommandLimits::approved_defaults().with_cpu_budget(Duration::from_secs(1)),
        )
        .unwrap();

        manager.recover_stale().unwrap();

        assert!(!stale.exists());
        assert_eq!(manager.fixture_kill_log(), [expected_stale]);
    }

    #[test]
    fn identities_are_fixed_length_hash_safe_components() {
        let identity = CommandIdentity::new("../namespace", "run/../../id", "command with spaces");
        for component in [
            &identity.namespace_hash,
            &identity.run_id_hash,
            &identity.command_id,
        ] {
            assert_eq!(component.len(), 20);
            assert!(component.bytes().all(|byte| byte.is_ascii_hexdigit()));
        }
    }

    #[test]
    fn cpu_usage_parses_usage_usec_exactly() {
        let root = fake_root("pids memory cpu");
        let manager =
            CgroupManager::validate_fixture(root.path(), CommandLimits::approved_defaults())
                .unwrap();
        let group = manager
            .create(&CommandIdentity::new("namespace", "run", "cpu"))
            .unwrap();
        fs::write(group.path().join("cpu.stat"), "usage_usec 123456\n").unwrap();

        assert_eq!(group.cpu_usage().unwrap(), Duration::from_micros(123_456));
    }

    #[tokio::test]
    async fn command_cleanup_writes_kill_waits_for_populated_zero_and_removes() {
        let root = fake_root("pids memory cpu");
        let manager =
            CgroupManager::validate_fixture(root.path(), CommandLimits::approved_defaults())
                .unwrap();
        let group = manager
            .create(&CommandIdentity::new("namespace", "run", "cleanup"))
            .unwrap();
        let path = group.path().to_path_buf();
        fs::write(path.join("cgroup.kill"), "0").unwrap();
        fs::write(path.join("cgroup.events"), "populated 0\n").unwrap();

        group.kill_wait_remove().await.unwrap();

        assert!(!path.exists());
        assert_eq!(manager.fixture_kill_log().len(), 1);
    }

    #[tokio::test]
    async fn command_cleanup_propagates_populated_timeout() {
        let root = fake_root("pids memory cpu");
        let mut limits = CommandLimits::approved_defaults();
        limits.cleanup_timeout = Duration::from_millis(20);
        let manager = CgroupManager::validate_fixture(root.path(), limits).unwrap();
        let group = manager
            .create(&CommandIdentity::new("namespace", "run", "stuck"))
            .unwrap();
        let path = group.path().to_path_buf();
        fs::write(path.join("cgroup.kill"), "0").unwrap();
        fs::write(path.join("cgroup.events"), "populated 1\n").unwrap();

        let error = group.kill_wait_remove().await.unwrap_err();

        assert!(error.to_string().contains("timed out waiting for cgroup"));
        assert!(path.exists());
    }

    #[test]
    fn aggregate_relationship_requires_shared_approved_ancestor() {
        let fixture = tempdir().unwrap();
        let aggregate = fixture.path().join("aggregate");
        let commands = aggregate.join("commands");
        let runtime = aggregate.join("runtime-service");
        fs::create_dir_all(&commands).unwrap();
        fs::create_dir(&runtime).unwrap();
        fs::write(aggregate.join("cgroup.controllers"), "pids memory cpu").unwrap();
        fs::write(aggregate.join("cgroup.subtree_control"), "pids memory cpu").unwrap();
        fs::write(aggregate.join("pids.max"), "512\n").unwrap();
        fs::write(aggregate.join("memory.max"), "1073741824\n").unwrap();
        fs::write(aggregate.join("memory.swap.max"), "0\n").unwrap();
        fs::write(aggregate.join("cpu.max"), "200000 100000\n").unwrap();
        let config = super::CgroupConfig {
            root: commands,
            limits: CommandLimits::approved_defaults(),
        };

        config
            .validate_aggregate_relationship(&aggregate, &runtime)
            .unwrap();

        let unrelated = fixture.path().join("unrelated");
        fs::create_dir(&unrelated).unwrap();
        assert!(
            config
                .validate_aggregate_relationship(&aggregate, &unrelated)
                .is_err()
        );
    }
}
