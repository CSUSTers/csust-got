#[cfg(target_os = "linux")]
fn main() -> anyhow::Result<()> {
    let mut args = std::env::args().skip(1);
    match args.next().as_deref() {
        Some("status") if args.next().is_none() => status(),
        Some("supervisor-dumpable") if args.next().is_none() => supervisor_dumpable(),
        Some("ptrace-attach") => ptrace_attach(args.next().as_deref(), args.next()),
        Some("socket") => socket_probe(args.next().as_deref(), args.next()),
        Some("unix-connect") if args.next().is_none() => unix_connect(),
        Some("syscall") => syscall_probe(args.next().as_deref(), args.next()),
        Some("clone") => clone_probe(args.next().as_deref(), args.next()),
        _ => anyhow::bail!("invalid probe arguments"),
    }
}

#[cfg(target_os = "linux")]
fn ptrace_attach(pid: Option<&str>, trailing: Option<String>) -> anyhow::Result<()> {
    if trailing.is_some() {
        anyhow::bail!("invalid ptrace attach arguments");
    }
    let pid = pid
        .ok_or_else(|| anyhow::anyhow!("missing ptrace target pid"))?
        .parse::<libc::pid_t>()?;
    if pid <= 0 {
        anyhow::bail!("ptrace target pid must be positive");
    }

    let result = unsafe {
        libc::ptrace(
            libc::PTRACE_ATTACH,
            pid,
            std::ptr::null_mut::<libc::c_void>(),
            std::ptr::null_mut::<libc::c_void>(),
        )
    };
    if result == -1 {
        return report_syscall(-1);
    }

    let mut status = 0_i32;
    let waited = loop {
        let waited = unsafe { libc::waitpid(pid, &mut status, 0) };
        if waited != -1 {
            break waited;
        }
        let error = std::io::Error::last_os_error();
        if error.raw_os_error() != Some(libc::EINTR) {
            break waited;
        }
    };
    let wait_error = if waited == pid && libc::WIFSTOPPED(status) {
        None
    } else if waited == -1 {
        Some(std::io::Error::last_os_error().to_string())
    } else {
        Some(format!("unexpected wait status {status} for pid {waited}"))
    };
    let detached = unsafe {
        libc::ptrace(
            libc::PTRACE_DETACH,
            pid,
            std::ptr::null_mut::<libc::c_void>(),
            std::ptr::null_mut::<libc::c_void>(),
        )
    };
    if let Some(error) = wait_error {
        return Err(anyhow::anyhow!(
            "ptrace attach unexpectedly succeeded but wait failed: {error}"
        ));
    }
    if detached == -1 {
        return Err(anyhow::anyhow!(
            "ptrace attach unexpectedly succeeded but detach failed: {}",
            std::io::Error::last_os_error()
        ));
    }
    anyhow::bail!("ptrace attach unexpectedly succeeded")
}

#[cfg(target_os = "linux")]
fn status() -> anyhow::Result<()> {
    let no_new_privs = unsafe { libc::prctl(libc::PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0) };
    if no_new_privs == -1 {
        return Err(std::io::Error::last_os_error().into());
    }
    println!(
        "no_new_privs={no_new_privs} fd9={} stdout={} stderr={}",
        fd_state(9),
        fd_state(libc::STDOUT_FILENO),
        fd_state(libc::STDERR_FILENO)
    );
    Ok(())
}

#[cfg(target_os = "linux")]
fn fd_state(fd: libc::c_int) -> &'static str {
    if unsafe { libc::fcntl(fd, libc::F_GETFD) } == -1
        && std::io::Error::last_os_error().raw_os_error() == Some(libc::EBADF)
    {
        "closed"
    } else {
        "open"
    }
}

#[cfg(target_os = "linux")]
fn supervisor_dumpable() -> anyhow::Result<()> {
    agent_runtime::sandbox::harden_runtime_supervisor()?;
    let dumpable = unsafe { libc::prctl(libc::PR_GET_DUMPABLE, 0, 0, 0, 0) };
    if dumpable == -1 {
        return Err(std::io::Error::last_os_error().into());
    }
    println!("dumpable={dumpable}");
    Ok(())
}

#[cfg(target_os = "linux")]
fn socket_probe(family: Option<&str>, trailing: Option<String>) -> anyhow::Result<()> {
    if trailing.is_some() {
        anyhow::bail!("invalid socket probe arguments");
    }
    let domain = match family {
        Some("inet") => libc::AF_INET,
        Some("inet6") => libc::AF_INET6,
        Some("packet") => libc::AF_PACKET,
        Some("netlink") => libc::AF_NETLINK,
        _ => anyhow::bail!("invalid socket family"),
    };
    report_syscall(unsafe { libc::syscall(libc::SYS_socket, domain, libc::SOCK_STREAM, 0) })
}

#[cfg(target_os = "linux")]
fn unix_connect() -> anyhow::Result<()> {
    use std::os::unix::net::{UnixListener, UnixStream};

    let directory = std::env::temp_dir().join(format!("agent-runtime-uds-{}", std::process::id()));
    std::fs::create_dir(&directory)?;
    let socket = directory.join("probe.sock");
    let listener = UnixListener::bind(&socket)?;
    let accepting = std::thread::spawn(move || listener.accept().map(|_| ()));
    let stream = UnixStream::connect(&socket)?;
    drop(stream);
    accepting
        .join()
        .map_err(|_| anyhow::anyhow!("Unix socket accept thread panicked"))??;
    std::fs::remove_file(socket)?;
    std::fs::remove_dir(directory)?;
    println!("unix-connect=ok");
    Ok(())
}

#[cfg(target_os = "linux")]
fn syscall_probe(syscall: Option<&str>, trailing: Option<String>) -> anyhow::Result<()> {
    if trailing.is_some() {
        anyhow::bail!("invalid syscall probe arguments");
    }
    let result = unsafe {
        match syscall {
            Some("setsid") => libc::syscall(libc::SYS_setsid),
            Some("setpgid") => libc::syscall(libc::SYS_setpgid, 0, 0),
            Some("unshare") => libc::syscall(libc::SYS_unshare, libc::CLONE_NEWNET),
            Some("setns") => libc::syscall(libc::SYS_setns, -1, libc::CLONE_NEWNET),
            Some("io_uring_setup") => {
                let mut parameters = [0_u64; 32];
                libc::syscall(libc::SYS_io_uring_setup, 1, parameters.as_mut_ptr())
            }
            Some("bpf") => libc::syscall(libc::SYS_bpf, 0, std::ptr::null::<u8>(), 0),
            Some("pidfd_getfd") => libc::syscall(libc::SYS_pidfd_getfd, -1, -1, 0),
            Some("clone3") => libc::syscall(libc::SYS_clone3, std::ptr::null::<u8>(), 0),
            _ => anyhow::bail!("invalid syscall name"),
        }
    };
    report_syscall(result)
}

#[cfg(target_os = "linux")]
fn clone_probe(flag: Option<&str>, trailing: Option<String>) -> anyhow::Result<()> {
    if trailing.is_some() {
        anyhow::bail!("invalid clone probe arguments");
    }
    let flag = flag
        .ok_or_else(|| anyhow::anyhow!("missing clone flag"))?
        .parse::<libc::c_long>()?;
    report_syscall(unsafe {
        libc::syscall(
            libc::SYS_clone,
            flag | libc::SIGCHLD as libc::c_long,
            0,
            0,
            0,
            0,
        )
    })
}

#[cfg(target_os = "linux")]
fn report_syscall(result: libc::c_long) -> anyhow::Result<()> {
    if result != -1 {
        anyhow::bail!("syscall unexpectedly succeeded with {result}");
    }
    let errno = std::io::Error::last_os_error()
        .raw_os_error()
        .ok_or_else(|| anyhow::anyhow!("syscall failed without errno"))?;
    println!("errno={errno}");
    Ok(())
}

#[cfg(not(target_os = "linux"))]
fn main() -> anyhow::Result<()> {
    anyhow::bail!("agent runtime network probe requires Linux")
}
