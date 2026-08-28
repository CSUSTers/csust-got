use super::SpawnControls;
use std::{
    mem::MaybeUninit,
    os::fd::{AsRawFd as _, OwnedFd, RawFd},
    sync::{Arc, Mutex},
};

const SPAWN_DESCRIPTOR_COUNT: usize = 5;

#[derive(Clone, Default)]
pub(in crate::exec) struct DescriptorReleaseProbe {
    descriptors: Arc<Mutex<Vec<DescriptorIdentity>>>,
}

impl DescriptorReleaseProbe {
    pub(in crate::exec) fn all_released(&self) -> bool {
        self.descriptors.lock().is_ok_and(|descriptors| {
            descriptors.len() == SPAWN_DESCRIPTOR_COUNT
                && descriptors.iter().all(DescriptorIdentity::is_released)
        })
    }

    fn observe(&self, descriptor: &OwnedFd) {
        let identity = DescriptorIdentity::capture(descriptor.as_raw_fd());
        if let Ok(mut descriptors) = self.descriptors.lock() {
            descriptors.push(identity);
        }
    }
}

pub(super) fn observe_spawn_descriptors(
    controls: &SpawnControls,
    descriptors: [&OwnedFd; SPAWN_DESCRIPTOR_COUNT],
) {
    if let Some(probe) = &controls.descriptor_probe {
        for descriptor in descriptors {
            probe.observe(descriptor);
        }
    }
}

struct DescriptorIdentity {
    descriptor: RawFd,
    device: libc::dev_t,
    inode: libc::ino_t,
    captured: bool,
}

impl DescriptorIdentity {
    fn capture(descriptor: RawFd) -> Self {
        let mut metadata = MaybeUninit::<libc::stat>::uninit();
        let captured = unsafe { libc::fstat(descriptor, metadata.as_mut_ptr()) } == 0;
        let (device, inode) = if captured {
            let metadata = unsafe { metadata.assume_init() };
            (metadata.st_dev, metadata.st_ino)
        } else {
            (0, 0)
        };
        Self {
            descriptor,
            device,
            inode,
            captured,
        }
    }

    fn is_released(&self) -> bool {
        if !self.captured {
            return false;
        }
        let mut metadata = MaybeUninit::<libc::stat>::uninit();
        if unsafe { libc::fstat(self.descriptor, metadata.as_mut_ptr()) } != 0 {
            return std::io::Error::last_os_error().raw_os_error() == Some(libc::EBADF);
        }
        let metadata = unsafe { metadata.assume_init() };
        metadata.st_dev != self.device || metadata.st_ino != self.inode
    }
}
