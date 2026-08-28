use super::{CommandBindingPhase, RuntimeFetchProxyError, output_error};
use crate::{
    exec::BashHealth,
    workspace_budget::{ReplaceReservation, WorkspaceBudget},
};
use cap_fs_ext::{FollowSymlinks, OpenOptionsFollowExt as _};
use cap_std::fs::{Dir, OpenOptions};
use std::{
    ffi::OsString,
    io::{self, Write as _},
    path::Path,
    sync::{Arc, Mutex},
};

mod path;
pub(super) use path::validate_workspace_output_path;
use path::{
    open_output_parent_nofollow, reject_cap_destination, sanitize_namespace, unique_temporary_name,
};

pub struct OutputCommitGuard {
    phase: Arc<Mutex<CommandBindingPhase>>,
    reservation: Option<ReplaceReservation>,
    parent: Dir,
    destination_name: OsString,
    temporary_name: OsString,
    file: Option<cap_std::fs::File>,
    total: u64,
    committed: bool,
    health: BashHealth,
    directory_sync: Arc<DirectorySync>,
    #[cfg(feature = "c7-test-support")]
    fault: Option<OutputFault>,
}

type DirectorySync = dyn Fn(&Dir) -> io::Result<()> + Send + Sync;

#[cfg(feature = "c7-test-support")]
#[derive(Clone, Copy)]
pub(in crate::runtime_fetch_proxy) enum OutputFault {
    Write,
    FileSync,
    Rename,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum OutputCommitOutcome {
    Committed,
}

impl OutputCommitGuard {
    pub fn new(
        workspace_root: &Path,
        namespace: &str,
        output_path: &str,
        budget: &WorkspaceBudget,
        phase: Arc<Mutex<CommandBindingPhase>>,
    ) -> Result<Self, RuntimeFetchProxyError> {
        Self::new_with_health(
            workspace_root,
            namespace,
            output_path,
            budget,
            phase,
            BashHealth::ready(),
        )
    }

    pub(crate) fn new_with_health(
        workspace_root: &Path,
        namespace: &str,
        output_path: &str,
        budget: &WorkspaceBudget,
        phase: Arc<Mutex<CommandBindingPhase>>,
        health: BashHealth,
    ) -> Result<Self, RuntimeFetchProxyError> {
        Self::new_inner(
            workspace_root,
            namespace,
            output_path,
            budget,
            phase,
            health,
            Arc::new(sync_cap_directory_io),
        )
    }

    fn new_inner(
        workspace_root: &Path,
        namespace: &str,
        output_path: &str,
        budget: &WorkspaceBudget,
        phase: Arc<Mutex<CommandBindingPhase>>,
        health: BashHealth,
        directory_sync: Arc<DirectorySync>,
    ) -> Result<Self, RuntimeFetchProxyError> {
        let relative = validate_workspace_output_path(output_path)?;
        let namespace = sanitize_namespace(namespace);
        let (parent, destination_name) =
            open_output_parent_nofollow(workspace_root, &namespace, &relative)?;
        let destination = workspace_root.join(namespace).join(&relative);
        reject_cap_destination(&parent, &destination_name)?;
        let reservation = budget
            .begin_replace(&destination)
            .map_err(|error| RuntimeFetchProxyError::policy(error.to_string()))?;
        let temporary_name = unique_temporary_name(&destination_name)?;
        let mut options = OpenOptions::new();
        options
            .create_new(true)
            .write(true)
            .follow(FollowSymlinks::No);
        let file = parent
            .open_with(Path::new(&temporary_name), &options)
            .map_err(output_error)?;
        Ok(Self {
            phase,
            reservation: Some(reservation),
            parent,
            destination_name,
            temporary_name,
            file: Some(file),
            total: 0,
            committed: false,
            health,
            directory_sync,
            #[cfg(feature = "c7-test-support")]
            fault: None,
        })
    }

    pub fn write_chunk(&mut self, bytes: &[u8]) -> Result<(), RuntimeFetchProxyError> {
        #[cfg(feature = "c7-test-support")]
        if matches!(self.fault, Some(OutputFault::Write)) {
            return Err(output_error(io::Error::other(
                "injected output write failure",
            )));
        }
        self.total = self
            .total
            .checked_add(bytes.len() as u64)
            .ok_or_else(|| RuntimeFetchProxyError::new("output length overflow"))?;
        self.reservation
            .as_mut()
            .ok_or_else(|| RuntimeFetchProxyError::new("output reservation is unavailable"))?
            .reserve_total(self.total)
            .map_err(|error| RuntimeFetchProxyError::policy(error.to_string()))?;
        self.file
            .as_mut()
            .ok_or_else(|| RuntimeFetchProxyError::new("output temporary file is unavailable"))?
            .write_all(bytes)
            .map_err(output_error)
    }

    pub fn commit_if_active(mut self) -> Result<OutputCommitOutcome, RuntimeFetchProxyError> {
        let file = self
            .file
            .take()
            .ok_or_else(|| RuntimeFetchProxyError::new("output temporary file is unavailable"))?;
        #[cfg(feature = "c7-test-support")]
        if matches!(self.fault, Some(OutputFault::FileSync)) {
            return Err(output_error(io::Error::other(
                "injected output file sync failure",
            )));
        }
        file.sync_all().map_err(output_error)?;
        drop(file);
        let phase_lock = Arc::clone(&self.phase);
        let phase = phase_lock
            .lock()
            .map_err(|_| RuntimeFetchProxyError::new("command binding phase is poisoned"))?;
        if *phase != CommandBindingPhase::Active {
            return Err(RuntimeFetchProxyError::policy(
                "command binding was revoked before output commit",
            ));
        }
        reject_cap_destination(&self.parent, &self.destination_name)?;
        #[cfg(feature = "c7-test-support")]
        if matches!(self.fault, Some(OutputFault::Rename)) {
            return Err(output_error(io::Error::other(
                "injected output rename failure",
            )));
        }
        self.parent
            .rename(
                Path::new(&self.temporary_name),
                &self.parent,
                Path::new(&self.destination_name),
            )
            .map_err(output_error)?;
        self.committed = true;
        if let Some(reservation) = self.reservation.take() {
            reservation.commit();
        }
        if (self.directory_sync)(&self.parent).is_err() {
            self.health.latch_workspace_durability_failure();
            tracing::warn!("workspace output directory sync failed after committed rename");
        }
        drop(phase);
        Ok(OutputCommitOutcome::Committed)
    }

    #[cfg(feature = "c7-test-support")]
    pub(in crate::runtime_fetch_proxy) fn set_fault(&mut self, fault: OutputFault) {
        self.fault = Some(fault);
    }

    #[cfg(feature = "c7-test-support")]
    pub(in crate::runtime_fetch_proxy) fn with_directory_sync_failure(
        workspace_root: &Path,
        namespace: &str,
        output_path: &str,
        budget: &WorkspaceBudget,
        phase: Arc<Mutex<CommandBindingPhase>>,
        health: BashHealth,
    ) -> Result<Self, RuntimeFetchProxyError> {
        Self::new_inner(
            workspace_root,
            namespace,
            output_path,
            budget,
            phase,
            health,
            Arc::new(|_| Err(io::Error::other("injected directory sync failure"))),
        )
    }
}

impl Drop for OutputCommitGuard {
    fn drop(&mut self) {
        if !self.committed {
            self.file.take();
            let _ = self.parent.remove_file(Path::new(&self.temporary_name));
        }
    }
}

#[cfg(unix)]
fn sync_cap_directory_io(directory: &Dir) -> io::Result<()> {
    directory
        .open(".")
        .and_then(|directory| directory.into_std().sync_all())
}

#[cfg(not(unix))]
fn sync_cap_directory_io(_directory: &Dir) -> io::Result<()> {
    Ok(())
}

#[cfg(test)]
mod tests;
