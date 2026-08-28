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
    if retained.is_empty() {
        let result = unsafe { libc::syscall(libc::SYS_close_range, 3_u32, u32::MAX, 0_u32) };
        if result == 0 {
            return Ok(());
        }
        let error = io::Error::last_os_error();
        if error.raw_os_error() != Some(libc::ENOSYS) {
            return Err(anyhow::anyhow!("close inherited file descriptors: {error}"));
        }
    }

    let upper = i32::try_from(hard_nofile)
        .map_err(|_| anyhow::anyhow!("hard RLIMIT_NOFILE exceeds the file descriptor range"))?;
    for fd in 3..upper {
        if retained.contains(&fd) {
            continue;
        }
        if unsafe { libc::close(fd) } != 0 {
            let error = io::Error::last_os_error();
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
    use super::{clone3_seccomp_rules, untrusted_seccomp_rules};

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
