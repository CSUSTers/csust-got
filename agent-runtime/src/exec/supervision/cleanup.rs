use super::super::*;

pub(in crate::exec) async fn cleanup_directory(directory: Option<&Path>) -> io::Result<()> {
    if let Some(directory) = directory {
        match tokio::fs::remove_dir_all(directory).await {
            Ok(()) => {}
            Err(error) if error.kind() == io::ErrorKind::NotFound => {}
            Err(error) => return Err(error),
        }
    }
    Ok(())
}

#[cfg(unix)]
pub(in crate::exec) fn cleanup_command_process_group(child_id: Option<u32>) -> Result<(), String> {
    use rustix::{
        io::Errno,
        process::{Pid, Signal, kill_process_group},
    };

    let Some(child_id) = child_id else {
        return Ok(());
    };
    let child_id = i32::try_from(child_id)
        .map_err(|_| format!("command process id {child_id} exceeds i32"))?;
    let pid = Pid::from_raw(child_id)
        .ok_or_else(|| format!("command process id {child_id} is not positive"))?;
    match kill_process_group(pid, Signal::KILL) {
        Ok(()) | Err(Errno::SRCH) => Ok(()),
        Err(error) => Err(error.to_string()),
    }
}

#[cfg(not(unix))]
pub(in crate::exec) fn cleanup_command_process_group(_child_id: Option<u32>) -> Result<(), String> {
    Ok(())
}
