use super::super::*;
use std::os::fd::OwnedFd;
use zeroize::Zeroizing;

pub(super) fn spawn(
    write_fd: OwnedFd,
    payload: Vec<u8>,
    #[allow(unused_variables)] fail_creation: bool,
) -> Result<std::thread::JoinHandle<io::Result<()>>, ExecError> {
    let payload = Zeroizing::new(payload);
    #[cfg(feature = "c7-test-support")]
    if fail_creation {
        return Err(ExecError::new("create exec config writer"));
    }
    std::thread::Builder::new()
        .name("exec-config-writer".to_string())
        .spawn(move || {
            let mut writer = std::fs::File::from(write_fd);
            writer.write_all(&payload)
        })
        .map_err(|_| ExecError::new("create exec config writer"))
}
