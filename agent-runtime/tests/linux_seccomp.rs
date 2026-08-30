#[cfg(target_os = "linux")]
mod linux_seccomp {
    use agent_runtime::exec::{
        EXEC_CONFIG_FD, EXEC_CONFIG_FLAG, ExecSpec, RlimitSpec, install_exec_fds,
    };
    use std::{
        fs,
        io::Read as _,
        os::{
            fd::{AsRawFd as _, FromRawFd as _, OwnedFd},
            unix::process::CommandExt as _,
        },
        path::{Path, PathBuf},
        process::{Command, Output, Stdio},
    };

    const EPERM: i32 = libc::EPERM;

    fn helper() -> PathBuf {
        PathBuf::from(env!("CARGO_BIN_EXE_agent-runtime-exec"))
    }

    fn probe() -> PathBuf {
        PathBuf::from(env!("CARGO_BIN_EXE_agent-runtime-net-probe"))
    }

    fn run_target(program: &Path, args: &[String], inherit_fd9: bool) -> Output {
        let fixture = tempfile::tempdir().unwrap();
        let cgroup_procs = fixture.path().join("cgroup.procs");
        fs::write(&cgroup_procs, b"").unwrap();
        let spec = ExecSpec {
            cgroup_procs,
            program: program.to_path_buf(),
            args: args.to_vec(),
            cwd: fixture.path().to_path_buf(),
            env: vec![
                (
                    "PATH".to_string(),
                    "/usr/local/bin:/usr/bin:/bin".to_string(),
                ),
                ("HOME".to_string(), "/tmp".to_string()),
            ],
            rlimits: RlimitSpec::approved_defaults(),
        };
        let config_path = fixture.path().join("exec.json");
        fs::write(&config_path, serde_json::to_vec(&spec).unwrap()).unwrap();
        let config = fs::File::open(config_path).unwrap();
        let config_fd = config.as_raw_fd();
        let mut control_fds = [-1; 2];
        assert_eq!(
            unsafe {
                libc::socketpair(
                    libc::AF_UNIX,
                    libc::SOCK_SEQPACKET | libc::SOCK_CLOEXEC,
                    0,
                    control_fds.as_mut_ptr(),
                )
            },
            0
        );
        let control_runtime = unsafe { OwnedFd::from_raw_fd(control_fds[0]) };
        let control_source = unsafe { OwnedFd::from_raw_fd(control_fds[1]) };
        let mut status_fds = [-1; 2];
        assert_eq!(
            unsafe { libc::pipe2(status_fds.as_mut_ptr(), libc::O_CLOEXEC) },
            0
        );
        let mut status_reader = unsafe { fs::File::from_raw_fd(status_fds[0]) };
        let status_source = unsafe { OwnedFd::from_raw_fd(status_fds[1]) };
        let control_fd = control_source.as_raw_fd();
        let status_fd = status_source.as_raw_fd();

        let mut command = Command::new(helper());
        command
            .args([EXEC_CONFIG_FLAG, &EXEC_CONFIG_FD.to_string()])
            .env_clear()
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        unsafe {
            command.pre_exec(move || {
                install_exec_fds(config_fd, control_fd, status_fd)?;
                if inherit_fd9 {
                    let extra_fd = libc::open(c"/dev/null".as_ptr(), libc::O_RDONLY);
                    if extra_fd == -1 {
                        return Err(std::io::Error::last_os_error());
                    }
                    if extra_fd != 9 {
                        if libc::dup2(extra_fd, 9) == -1 {
                            return Err(std::io::Error::last_os_error());
                        }
                        libc::close(extra_fd);
                    }
                }
                Ok(())
            });
        }
        let child = command.spawn().unwrap();
        drop(control_runtime);
        drop(control_source);
        drop(status_source);
        let output = child.wait_with_output().unwrap();
        let mut status_payload = Vec::new();
        status_reader.read_to_end(&mut status_payload).unwrap();
        assert!(
            status_payload.is_empty(),
            "helper enforcement status frame: {status_payload:?}"
        );
        output
    }

    fn run_probe(args: &[&str]) -> Output {
        run_target(
            &probe(),
            &args
                .iter()
                .map(|arg| (*arg).to_string())
                .collect::<Vec<_>>(),
            false,
        )
    }

    fn assert_probe_errno(args: &[&str], expected_errno: i32) {
        let output = run_probe(args);
        assert!(
            output.status.success(),
            "probe {args:?} failed: {}",
            String::from_utf8_lossy(&output.stderr)
        );
        assert_eq!(
            String::from_utf8_lossy(&output.stdout).trim(),
            format!("errno={expected_errno}"),
            "probe {args:?} returned unexpected output"
        );
    }

    #[test]
    fn helper_closes_inherited_fds_and_sets_no_new_privs() {
        let output = run_target(&probe(), &["status".to_string()], true);
        assert!(
            output.status.success(),
            "{}",
            String::from_utf8_lossy(&output.stderr)
        );
        assert_eq!(
            String::from_utf8_lossy(&output.stdout).trim(),
            "no_new_privs=1 fd9=closed stdout=open stderr=open"
        );
    }

    #[test]
    fn runtime_supervisor_is_not_dumpable() {
        let output = Command::new(probe())
            .arg("supervisor-dumpable")
            .output()
            .unwrap();
        assert!(
            output.status.success(),
            "{}",
            String::from_utf8_lossy(&output.stderr)
        );
        assert_eq!(String::from_utf8_lossy(&output.stdout).trim(), "dumpable=0");
    }

    #[test]
    fn ptrace_attach_rejects_invalid_arguments_without_attaching() {
        for args in [
            Vec::<&str>::new(),
            vec!["0"],
            vec!["-1"],
            vec!["not-a-pid"],
            vec!["1", "trailing"],
        ] {
            let output = Command::new(probe())
                .arg("ptrace-attach")
                .args(&args)
                .output()
                .unwrap();
            assert!(
                !output.status.success(),
                "invalid ptrace arguments unexpectedly succeeded: {args:?}"
            );
        }
    }

    #[test]
    fn internet_socket_families_are_denied_but_unix_connects() {
        for family in ["inet", "inet6", "packet", "netlink"] {
            assert_probe_errno(&["socket", family], EPERM);
        }
        let output = run_probe(&["unix-connect"]);
        assert!(
            output.status.success(),
            "{}",
            String::from_utf8_lossy(&output.stderr)
        );
        assert_eq!(
            String::from_utf8_lossy(&output.stdout).trim(),
            "unix-connect=ok"
        );
    }

    #[test]
    fn privileged_process_control_syscalls_are_denied() {
        for syscall in [
            "setsid",
            "setpgid",
            "unshare",
            "setns",
            "io_uring_setup",
            "bpf",
            "pidfd_getfd",
        ] {
            assert_probe_errno(&["syscall", syscall], EPERM);
        }
        assert_probe_errno(&["syscall", "clone3"], libc::ENOSYS);
    }

    #[test]
    fn every_namespace_clone_flag_is_denied() {
        for flag in [
            libc::CLONE_NEWCGROUP,
            libc::CLONE_NEWIPC,
            libc::CLONE_NEWNET,
            libc::CLONE_NEWNS,
            libc::CLONE_NEWPID,
            libc::CLONE_NEWTIME,
            libc::CLONE_NEWUSER,
            libc::CLONE_NEWUTS,
        ] {
            assert_probe_errno(&["clone", &flag.to_string()], EPERM);
        }
    }

    #[test]
    fn bash_fork_exec_pipe_and_local_git_still_work() {
        let command = concat!(
            "set -eu; ",
            "child=$(bash -c 'printf child'); test \"$child\" = child; ",
            "printf pipe-ok | grep -q '^pipe-ok$'; ",
            "git init -q repo; git -C repo status --porcelain; printf compatibility-ok"
        );
        let output = run_target(
            Path::new("/bin/bash"),
            &["-lc".to_string(), command.to_string()],
            false,
        );
        assert!(
            output.status.success(),
            "{}",
            String::from_utf8_lossy(&output.stderr)
        );
        assert_eq!(String::from_utf8_lossy(&output.stdout), "compatibility-ok");
    }

    #[test]
    fn proot_still_works_when_available() {
        if Command::new("proot").arg("--version").output().is_err() {
            eprintln!("proot is unavailable; runtime-image coverage remains required");
            return;
        }
        let output = run_target(
            Path::new("proot"),
            &[
                "/bin/bash".to_string(),
                "-lc".to_string(),
                "printf proot-ok".to_string(),
            ],
            false,
        );
        assert!(
            output.status.success(),
            "{}",
            String::from_utf8_lossy(&output.stderr)
        );
        assert_eq!(String::from_utf8_lossy(&output.stdout), "proot-ok");
    }
}
