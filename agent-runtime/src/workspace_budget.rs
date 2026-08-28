use std::{
    collections::BTreeSet,
    fmt, fs, io,
    path::{Component, Path, PathBuf},
    sync::{Arc, Mutex},
};
use walkdir::WalkDir;

type CapacityProbe = dyn Fn(&Path) -> io::Result<u64> + Send + Sync;

#[derive(Clone)]
pub struct WorkspaceBudget {
    inner: Arc<BudgetInner>,
}

struct BudgetInner {
    root: PathBuf,
    max_bytes: u64,
    state: Mutex<BudgetState>,
    capacity_probe: Arc<CapacityProbe>,
}

#[derive(Default)]
struct BudgetState {
    baseline_bytes: Option<u64>,
    pending_growth: u64,
    destinations: BTreeSet<PathBuf>,
}

pub struct ReplaceReservation {
    inner: Arc<BudgetInner>,
    destination: PathBuf,
    old_len: u64,
    new_len: u64,
    growth: u64,
    active: bool,
}

pub type Reservation = ReplaceReservation;

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct WorkspaceBudgetError {
    kind: WorkspaceBudgetErrorKind,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum WorkspaceBudgetErrorKind {
    InvalidLimit,
    InvalidPath,
    DestinationBusy,
    Inspect,
    LogicalCapacity,
    FilesystemCapacity,
}

impl WorkspaceBudget {
    pub fn new(root: impl AsRef<Path>, max_bytes: u64) -> Result<Self, WorkspaceBudgetError> {
        Self::with_capacity_probe(root, max_bytes, Arc::new(|path| fs2::available_space(path)))
    }

    pub fn with_capacity_probe(
        root: impl AsRef<Path>,
        max_bytes: u64,
        capacity_probe: Arc<CapacityProbe>,
    ) -> Result<Self, WorkspaceBudgetError> {
        if max_bytes == 0 {
            return Err(WorkspaceBudgetError::new(
                WorkspaceBudgetErrorKind::InvalidLimit,
            ));
        }
        let root = if root.as_ref().is_absolute() {
            root.as_ref().to_path_buf()
        } else {
            std::env::current_dir()
                .map_err(|_| WorkspaceBudgetError::new(WorkspaceBudgetErrorKind::Inspect))?
                .join(root)
        };
        fs::create_dir_all(&root)
            .map_err(|_| WorkspaceBudgetError::new(WorkspaceBudgetErrorKind::Inspect))?;
        if fs::symlink_metadata(&root)
            .map_err(|_| WorkspaceBudgetError::new(WorkspaceBudgetErrorKind::Inspect))?
            .file_type()
            .is_symlink()
        {
            return Err(WorkspaceBudgetError::new(
                WorkspaceBudgetErrorKind::InvalidPath,
            ));
        }
        Ok(Self {
            inner: Arc::new(BudgetInner {
                root,
                max_bytes,
                state: Mutex::new(BudgetState::default()),
                capacity_probe,
            }),
        })
    }

    pub fn begin_replace(
        &self,
        path: impl AsRef<Path>,
    ) -> Result<ReplaceReservation, WorkspaceBudgetError> {
        let destination = self.checked_path(path.as_ref())?;
        let mut state = self
            .inner
            .state
            .lock()
            .map_err(|_| WorkspaceBudgetError::new(WorkspaceBudgetErrorKind::Inspect))?;
        if state.destinations.contains(&destination) {
            return Err(WorkspaceBudgetError::new(
                WorkspaceBudgetErrorKind::DestinationBusy,
            ));
        }
        let old_len = existing_file_len(&destination)?;
        if state.destinations.is_empty() {
            state.baseline_bytes = Some(workspace_size(&self.inner.root)?);
        }
        state.destinations.insert(destination.clone());
        Ok(ReplaceReservation {
            inner: Arc::clone(&self.inner),
            destination,
            old_len,
            new_len: old_len,
            growth: 0,
            active: true,
        })
    }

    pub fn reserve_replace(
        &self,
        path: impl AsRef<Path>,
        new_len: u64,
    ) -> Result<ReplaceReservation, WorkspaceBudgetError> {
        let mut reservation = self.begin_replace(path)?;
        reservation.reserve_total(new_len)?;
        Ok(reservation)
    }

    pub fn root(&self) -> &Path {
        &self.inner.root
    }

    pub fn max_bytes(&self) -> u64 {
        self.inner.max_bytes
    }

    fn checked_path(&self, path: &Path) -> Result<PathBuf, WorkspaceBudgetError> {
        if path
            .components()
            .any(|component| matches!(component, Component::ParentDir))
        {
            return Err(WorkspaceBudgetError::new(
                WorkspaceBudgetErrorKind::InvalidPath,
            ));
        }
        let path = if path.is_absolute() {
            path.to_path_buf()
        } else {
            self.inner.root.join(path)
        };
        if path == self.inner.root || !path.starts_with(&self.inner.root) {
            return Err(WorkspaceBudgetError::new(
                WorkspaceBudgetErrorKind::InvalidPath,
            ));
        }
        let relative = path
            .strip_prefix(&self.inner.root)
            .map_err(|_| WorkspaceBudgetError::new(WorkspaceBudgetErrorKind::InvalidPath))?;
        let mut current = self.inner.root.clone();
        for component in relative.components() {
            let Component::Normal(name) = component else {
                continue;
            };
            current.push(name);
            match fs::symlink_metadata(&current) {
                Ok(metadata) if metadata.file_type().is_symlink() => {
                    return Err(WorkspaceBudgetError::new(
                        WorkspaceBudgetErrorKind::InvalidPath,
                    ));
                }
                Ok(_) => {}
                Err(error) if error.kind() == io::ErrorKind::NotFound => break,
                Err(_) => {
                    return Err(WorkspaceBudgetError::new(WorkspaceBudgetErrorKind::Inspect));
                }
            }
        }
        Ok(path)
    }
}

impl ReplaceReservation {
    pub fn reserve_total(&mut self, new_len: u64) -> Result<(), WorkspaceBudgetError> {
        if !self.active {
            return Err(WorkspaceBudgetError::new(WorkspaceBudgetErrorKind::Inspect));
        }
        self.new_len = new_len;
        let wanted_growth = new_len.saturating_sub(self.old_len);
        if wanted_growth <= self.growth {
            return Ok(());
        }
        let additional = wanted_growth - self.growth;
        let mut state = self
            .inner
            .state
            .lock()
            .map_err(|_| WorkspaceBudgetError::new(WorkspaceBudgetErrorKind::Inspect))?;
        let baseline = state
            .baseline_bytes
            .ok_or_else(|| WorkspaceBudgetError::new(WorkspaceBudgetErrorKind::Inspect))?;
        let reserved_total = baseline
            .checked_add(state.pending_growth)
            .and_then(|value| value.checked_add(additional))
            .ok_or_else(|| WorkspaceBudgetError::new(WorkspaceBudgetErrorKind::LogicalCapacity))?;
        if reserved_total > self.inner.max_bytes {
            return Err(WorkspaceBudgetError::new(
                WorkspaceBudgetErrorKind::LogicalCapacity,
            ));
        }
        let available = (self.inner.capacity_probe)(&self.inner.root)
            .map_err(|_| WorkspaceBudgetError::new(WorkspaceBudgetErrorKind::Inspect))?;
        let required_free = state
            .pending_growth
            .checked_add(additional)
            .ok_or_else(|| {
                WorkspaceBudgetError::new(WorkspaceBudgetErrorKind::FilesystemCapacity)
            })?;
        if required_free > available {
            return Err(WorkspaceBudgetError::new(
                WorkspaceBudgetErrorKind::FilesystemCapacity,
            ));
        }
        state.pending_growth = required_free;
        self.growth = wanted_growth;
        Ok(())
    }

    pub fn commit(mut self) {
        self.release(true);
    }

    pub fn destination(&self) -> &Path {
        &self.destination
    }

    fn release(&mut self, committed: bool) {
        if !self.active {
            return;
        }
        if let Ok(mut state) = self.inner.state.lock() {
            if committed && let Some(baseline) = state.baseline_bytes.as_mut() {
                *baseline = baseline
                    .saturating_sub(self.old_len)
                    .saturating_add(self.new_len);
            }
            state.pending_growth = state.pending_growth.saturating_sub(self.growth);
            state.destinations.remove(&self.destination);
            if state.destinations.is_empty() {
                state.baseline_bytes = None;
            }
        }
        self.active = false;
    }
}

impl Drop for ReplaceReservation {
    fn drop(&mut self) {
        self.release(false);
    }
}

impl fmt::Debug for ReplaceReservation {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter
            .debug_struct("ReplaceReservation")
            .field("destination", &self.destination)
            .field("old_len", &self.old_len)
            .field("new_len", &self.new_len)
            .field("growth", &self.growth)
            .field("active", &self.active)
            .finish()
    }
}

impl fmt::Debug for WorkspaceBudget {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter
            .debug_struct("WorkspaceBudget")
            .field("root", &self.inner.root)
            .field("max_bytes", &self.inner.max_bytes)
            .finish()
    }
}

impl WorkspaceBudgetError {
    fn new(kind: WorkspaceBudgetErrorKind) -> Self {
        Self { kind }
    }

    pub fn is_invalid_path(&self) -> bool {
        self.kind == WorkspaceBudgetErrorKind::InvalidPath
    }
}

impl fmt::Display for WorkspaceBudgetError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(match self.kind {
            WorkspaceBudgetErrorKind::InvalidLimit => {
                "workspace capacity limit must be greater than zero"
            }
            WorkspaceBudgetErrorKind::InvalidPath => "workspace budget path is outside its root",
            WorkspaceBudgetErrorKind::DestinationBusy => {
                "workspace destination already has an active replacement"
            }
            WorkspaceBudgetErrorKind::Inspect => "workspace capacity could not be inspected",
            WorkspaceBudgetErrorKind::LogicalCapacity => "workspace capacity limit exceeded",
            WorkspaceBudgetErrorKind::FilesystemCapacity => {
                "underlying filesystem capacity is insufficient"
            }
        })
    }
}

impl std::error::Error for WorkspaceBudgetError {}

fn workspace_size(root: &Path) -> Result<u64, WorkspaceBudgetError> {
    let mut total = 0_u64;
    for entry in WalkDir::new(root).follow_links(false) {
        let entry =
            entry.map_err(|_| WorkspaceBudgetError::new(WorkspaceBudgetErrorKind::Inspect))?;
        if entry.file_type().is_file() {
            total = total
                .checked_add(
                    entry
                        .metadata()
                        .map_err(|_| WorkspaceBudgetError::new(WorkspaceBudgetErrorKind::Inspect))?
                        .len(),
                )
                .ok_or_else(|| {
                    WorkspaceBudgetError::new(WorkspaceBudgetErrorKind::LogicalCapacity)
                })?;
        }
    }
    Ok(total)
}

fn existing_file_len(path: &Path) -> Result<u64, WorkspaceBudgetError> {
    match fs::symlink_metadata(path) {
        Ok(metadata) if metadata.file_type().is_file() => Ok(metadata.len()),
        Ok(_) => Err(WorkspaceBudgetError::new(
            WorkspaceBudgetErrorKind::InvalidPath,
        )),
        Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(0),
        Err(_) => Err(WorkspaceBudgetError::new(WorkspaceBudgetErrorKind::Inspect)),
    }
}
