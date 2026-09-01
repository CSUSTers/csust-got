#[cfg(target_os = "linux")]
use std::{collections::BTreeMap, io};

pub fn harden_runtime_supervisor() -> anyhow::Result<()> {
    #[cfg(target_os = "linux")]
    if unsafe { libc::prctl(libc::PR_SET_DUMPABLE, 0, 0, 0, 0) } != 0 {
        return Err(anyhow::anyhow!(
            "set runtime supervisor non-dumpable: {}",
            io::Error::last_os_error()
        ));
    }
    Ok(())
}

pub fn runtime_supervisor_dumpable() -> anyhow::Result<bool> {
    #[cfg(target_os = "linux")]
    {
        let value = unsafe { libc::prctl(libc::PR_GET_DUMPABLE, 0, 0, 0, 0) };
        if value < 0 {
            return Err(anyhow::anyhow!(
                "read runtime supervisor dumpable status: {}",
                io::Error::last_os_error()
            ));
        }
        Ok(value != 0)
    }
    #[cfg(not(target_os = "linux"))]
    {
        Ok(false)
    }
}

#[cfg(target_os = "linux")]
pub(crate) fn capture_hard_nofile() -> anyhow::Result<libc::rlim_t> {
    let mut limit = libc::rlimit {
        rlim_cur: 0,
        rlim_max: 0,
    };
    if unsafe { libc::getrlimit(libc::RLIMIT_NOFILE, &mut limit) } != 0 {
        return Err(anyhow::anyhow!(
            "get hard RLIMIT_NOFILE: {}",
            io::Error::last_os_error()
        ));
    }
    if limit.rlim_max == libc::RLIM_INFINITY {
        anyhow::bail!("hard RLIMIT_NOFILE must be finite");
    }
    Ok(limit.rlim_max)
}

#[cfg(target_os = "linux")]
pub(crate) fn close_inherited_fds_except(
    hard_nofile: libc::rlim_t,
    retained: &[i32],
) -> anyhow::Result<()> {
    close_inherited_fds_except_with(&mut RealCloseRangeSyscalls, hard_nofile, retained)
}

#[cfg(target_os = "linux")]
trait CloseRangeSyscalls {
    fn close_range(&mut self, first: u32, last: u32) -> io::Result<()>;
    fn close(&mut self, fd: i32) -> io::Result<()>;
}

#[cfg(target_os = "linux")]
struct RealCloseRangeSyscalls;

#[cfg(target_os = "linux")]
impl CloseRangeSyscalls for RealCloseRangeSyscalls {
    fn close_range(&mut self, first: u32, last: u32) -> io::Result<()> {
        if unsafe { libc::syscall(libc::SYS_close_range, first, last, 0_u32) } == 0 {
            Ok(())
        } else {
            Err(io::Error::last_os_error())
        }
    }

    fn close(&mut self, fd: i32) -> io::Result<()> {
        if unsafe { libc::close(fd) } == 0 {
            Ok(())
        } else {
            Err(io::Error::last_os_error())
        }
    }
}

#[cfg(target_os = "linux")]
fn close_inherited_fds_except_with<S: CloseRangeSyscalls>(
    syscalls: &mut S,
    hard_nofile: libc::rlim_t,
    retained: &[i32],
) -> anyhow::Result<()> {
    let mut retained = retained
        .iter()
        .copied()
        .filter(|fd| *fd >= 3)
        .collect::<Vec<_>>();
    retained.sort_unstable();
    retained.dedup();

    match close_inherited_fds_with_close_range(syscalls, &retained) {
        Ok(()) => Ok(()),
        Err(error) if error.raw_os_error() == Some(libc::ENOSYS) => {
            close_inherited_fds_with_loop(syscalls, hard_nofile, &retained)
        }
        Err(error) => Err(anyhow::anyhow!("close inherited file descriptors: {error}")),
    }
}

#[cfg(target_os = "linux")]
fn close_inherited_fds_with_close_range<S: CloseRangeSyscalls>(
    syscalls: &mut S,
    retained: &[i32],
) -> io::Result<()> {
    let mut first = 3_u32;
    for retained_fd in retained {
        let retained_fd = *retained_fd as u32;
        if first < retained_fd {
            syscalls.close_range(first, retained_fd - 1)?;
        }
        first = retained_fd.saturating_add(1);
    }
    syscalls.close_range(first, u32::MAX)?;
    Ok(())
}

#[cfg(target_os = "linux")]
fn close_inherited_fds_with_loop<S: CloseRangeSyscalls>(
    syscalls: &mut S,
    hard_nofile: libc::rlim_t,
    retained: &[i32],
) -> anyhow::Result<()> {
    if hard_nofile == libc::RLIM_INFINITY {
        anyhow::bail!("hard RLIMIT_NOFILE must be finite");
    }
    let upper = i32::try_from(hard_nofile)
        .map_err(|_| anyhow::anyhow!("hard RLIMIT_NOFILE exceeds the file descriptor range"))?;
    for fd in 3..upper {
        if retained.binary_search(&fd).is_ok() {
            continue;
        }
        if let Err(error) = syscalls.close(fd) {
            if error.raw_os_error() != Some(libc::EBADF) {
                return Err(anyhow::anyhow!(
                    "close inherited file descriptor {fd}: {error}"
                ));
            }
        }
    }
    Ok(())
}

#[cfg(target_os = "linux")]
pub(crate) fn set_no_new_privs() -> anyhow::Result<()> {
    if unsafe { libc::prctl(libc::PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) } != 0 {
        return Err(anyhow::anyhow!(
            "set no_new_privs: {}",
            io::Error::last_os_error()
        ));
    }
    Ok(())
}

#[cfg(target_os = "linux")]
pub(crate) fn apply_untrusted_seccomp() -> anyhow::Result<()> {
    let filters = build_untrusted_seccomp()?;
    apply_untrusted_seccomp_filters(&filters)
}

#[cfg(target_os = "linux")]
pub(crate) struct UntrustedSeccomp {
    clone3: seccompiler::BpfProgram,
    full: seccompiler::BpfProgram,
}

#[cfg(target_os = "linux")]
pub(crate) fn build_untrusted_seccomp() -> anyhow::Result<UntrustedSeccomp> {
    Ok(UntrustedSeccomp {
        clone3: seccomp_filter(
            clone3_seccomp_rules(),
            seccompiler::SeccompAction::Errno(libc::ENOSYS as u32),
        )?,
        full: seccomp_filter(
            untrusted_seccomp_rules()?,
            seccompiler::SeccompAction::Errno(libc::EPERM as u32),
        )?,
    })
}

#[cfg(target_os = "linux")]
pub(crate) fn apply_untrusted_seccomp_filters(filters: &UntrustedSeccomp) -> anyhow::Result<()> {
    seccompiler::apply_filter(&filters.clone3)
        .map_err(|error| anyhow::anyhow!("apply clone3 seccomp: {error}"))?;
    seccompiler::apply_filter(&filters.full)
        .map_err(|error| anyhow::anyhow!("apply untrusted command seccomp: {error}"))
}

#[cfg(target_os = "linux")]
fn seccomp_filter(
    rules: BTreeMap<i64, Vec<seccompiler::SeccompRule>>,
    match_action: seccompiler::SeccompAction,
) -> anyhow::Result<seccompiler::BpfProgram> {
    use seccompiler::{SeccompAction, SeccompFilter, TargetArch};

    let target_arch = match std::env::consts::ARCH {
        "x86_64" => TargetArch::x86_64,
        "aarch64" => TargetArch::aarch64,
        arch => anyhow::bail!("command seccomp is unsupported on Linux architecture {arch}"),
    };
    let filter = SeccompFilter::new(rules, SeccompAction::Allow, match_action, target_arch)?;
    filter.try_into().map_err(Into::into)
}

#[cfg(target_os = "linux")]
fn untrusted_seccomp_rules() -> anyhow::Result<BTreeMap<i64, Vec<seccompiler::SeccompRule>>> {
    use seccompiler::{SeccompCmpArgLen, SeccompCmpOp, SeccompCondition, SeccompRule};

    let socket_rule = SeccompRule::new(vec![SeccompCondition::new(
        0,
        SeccompCmpArgLen::Dword,
        SeccompCmpOp::Ne,
        libc::AF_UNIX as u64,
    )?])?;
    let clone_rules = [
        libc::CLONE_NEWCGROUP,
        libc::CLONE_NEWIPC,
        libc::CLONE_NEWNET,
        libc::CLONE_NEWNS,
        libc::CLONE_NEWPID,
        libc::CLONE_NEWTIME,
        libc::CLONE_NEWUSER,
        libc::CLONE_NEWUTS,
    ]
    .into_iter()
    .map(|flag| {
        Ok(SeccompRule::new(vec![SeccompCondition::new(
            0,
            SeccompCmpArgLen::Qword,
            SeccompCmpOp::MaskedEq(flag as u64),
            flag as u64,
        )?])?)
    })
    .collect::<anyhow::Result<Vec<_>>>()?;

    let mut rules = BTreeMap::new();
    insert_syscall(&mut rules, libc::SYS_socket as i64, vec![socket_rule]);
    insert_syscall(&mut rules, libc::SYS_clone as i64, clone_rules);
    for syscall in [
        libc::SYS_setsid,
        libc::SYS_setpgid,
        libc::SYS_unshare,
        libc::SYS_setns,
        libc::SYS_io_uring_setup,
        libc::SYS_bpf,
        libc::SYS_pidfd_getfd,
    ] {
        insert_syscall(&mut rules, syscall, Vec::new());
    }
    Ok(rules)
}

#[cfg(target_os = "linux")]
fn clone3_seccomp_rules() -> BTreeMap<i64, Vec<seccompiler::SeccompRule>> {
    let mut rules = BTreeMap::new();
    insert_syscall(&mut rules, libc::SYS_clone3, Vec::new());
    rules
}

#[cfg(target_os = "linux")]
fn insert_syscall(
    rules: &mut BTreeMap<i64, Vec<seccompiler::SeccompRule>>,
    syscall: i64,
    syscall_rules: Vec<seccompiler::SeccompRule>,
) {
    rules.insert(syscall, syscall_rules.clone());
    #[cfg(target_arch = "x86_64")]
    rules.insert(syscall | 0x4000_0000, syscall_rules);
}

#[cfg(all(test, target_os = "linux"))]
mod tests {
    use super::{
        CloseRangeSyscalls, clone3_seccomp_rules, close_inherited_fds_except_with,
        untrusted_seccomp_rules,
    };
    use std::{collections::VecDeque, io};

    struct FakeCloseRangeSyscalls {
        close_range_results: VecDeque<i32>,
        close_results: VecDeque<i32>,
        close_range_calls: Vec<(u32, u32)>,
        close_calls: Vec<i32>,
    }

    impl FakeCloseRangeSyscalls {
        fn result(results: &mut VecDeque<i32>) -> io::Result<()> {
            match results.pop_front().unwrap_or(0) {
                0 => Ok(()),
                error => Err(io::Error::from_raw_os_error(error)),
            }
        }
    }

    impl CloseRangeSyscalls for FakeCloseRangeSyscalls {
        fn close_range(&mut self, first: u32, last: u32) -> io::Result<()> {
            self.close_range_calls.push((first, last));
            Self::result(&mut self.close_range_results)
        }

        fn close(&mut self, fd: i32) -> io::Result<()> {
            self.close_calls.push(fd);
            Self::result(&mut self.close_results)
        }
    }

    #[test]
    fn close_range_splits_around_sorted_unique_retained_descriptors() {
        let mut syscalls = FakeCloseRangeSyscalls {
            close_range_results: VecDeque::from(vec![0, 0]),
            close_results: VecDeque::new(),
            close_range_calls: Vec::new(),
            close_calls: Vec::new(),
        };

        close_inherited_fds_except_with(&mut syscalls, 512, &[5, 4, 5, 2, -1]).unwrap();

        assert_eq!(syscalls.close_range_calls, [(3, 3), (6, u32::MAX)]);
        assert!(syscalls.close_calls.is_empty());
    }

    #[test]
    fn close_range_enosys_falls_back_to_the_captured_hard_limit() {
        let mut syscalls = FakeCloseRangeSyscalls {
            close_range_results: VecDeque::from(vec![libc::ENOSYS]),
            close_results: VecDeque::from(vec![libc::EBADF, 0, 0]),
            close_range_calls: Vec::new(),
            close_calls: Vec::new(),
        };

        close_inherited_fds_except_with(&mut syscalls, 8, &[4, 5]).unwrap();

        assert_eq!(syscalls.close_range_calls, [(3, 3)]);
        assert_eq!(syscalls.close_calls, [3, 6, 7]);
    }

    #[test]
    fn close_range_non_enosys_failure_fails_closed() {
        let mut syscalls = FakeCloseRangeSyscalls {
            close_range_results: VecDeque::from(vec![libc::EPERM]),
            close_results: VecDeque::new(),
            close_range_calls: Vec::new(),
            close_calls: Vec::new(),
        };

        let error = close_inherited_fds_except_with(&mut syscalls, 8, &[4, 5]).unwrap_err();

        assert!(
            error
                .to_string()
                .contains("close inherited file descriptors")
        );
        assert_eq!(syscalls.close_range_calls, [(3, 3)]);
        assert!(syscalls.close_calls.is_empty());
    }

    #[test]
    fn rules_cover_native_and_x32_without_blocking_proot_syscalls() {
        let rules = untrusted_seccomp_rules().unwrap();
        for syscall in [
            libc::SYS_socket,
            libc::SYS_clone,
            libc::SYS_setsid,
            libc::SYS_setpgid,
            libc::SYS_unshare,
            libc::SYS_setns,
            libc::SYS_io_uring_setup,
            libc::SYS_bpf,
            libc::SYS_pidfd_getfd,
        ] {
            assert!(rules.contains_key(&syscall));
            #[cfg(target_arch = "x86_64")]
            assert!(rules.contains_key(&(syscall | 0x4000_0000)));
        }
        assert_eq!(rules[&libc::SYS_socket].len(), 1);
        assert_eq!(rules[&libc::SYS_clone].len(), 8);
        assert!(!rules.contains_key(&libc::SYS_ptrace));
        assert!(!rules.contains_key(&libc::SYS_process_vm_readv));
        assert!(!rules.contains_key(&libc::SYS_process_vm_writev));

        let clone3 = clone3_seccomp_rules();
        assert!(clone3.contains_key(&libc::SYS_clone3));
        #[cfg(target_arch = "x86_64")]
        assert!(clone3.contains_key(&(libc::SYS_clone3 | 0x4000_0000)));
    }
}
