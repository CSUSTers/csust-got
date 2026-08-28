#[cfg(target_os = "linux")]
mod linux_exec_helper {
    use agent_runtime::exec::{
        COMMAND_CONTROL_FD, EXEC_CONFIG_FD, EXEC_STATUS_FD, ExecSpec, ExecStartupOutcome,
        RlimitSpec, install_exec_fds, spawn_exec_helper,
    };
    use std::{
        io,
        mem::MaybeUninit,
        panic::{AssertUnwindSafe, catch_unwind},
        path::PathBuf,
        time::Duration,
    };

    const CONFIG_SENTINEL: &[u8] = b"config-sentinel";
    const CONTROL_NONCE: &[u8] = b"control-nonce";

    pub(super) fn scenario_exec_fd_layouts_preserve_config_control_status() {
        for layout in [
            (3, 4, 5),
            (3, 5, 4),
            (4, 3, 5),
            (4, 5, 3),
            (5, 3, 4),
            (5, 4, 3),
            (3, 8, 9),
            (8, 4, 9),
            (8, 9, 5),
            (8, 9, 10),
        ] {
            run_child(move || verify_layout(layout));
        }
    }

    pub(super) async fn scenario_each_three_fd_mapping_stage_failure_aborts_and_latches() {
        #[cfg(feature = "c7-test-support")]
        {
            let receipt = agent_runtime::c7_test_support::fd_mapping_fault_table().await;
            assert!(receipt.success_control_marker);
            assert_eq!(receipt.rows.len(), 21);
            assert_eq!(
                receipt.rows.iter().map(|row| row.stage).collect::<Vec<_>>(),
                agent_runtime::c7_test_support::FD_INSTALL_FAULT_STAGES
            );
            eprintln!(
                "stage\tfailed\tmarker\thealth\treason-stable\tbinding\tregistry\tcgroup\tcleanup\tdeferred\tfds\tsubsequent"
            );
            for row in receipt.rows {
                eprintln!(
                    "{}\t{}\t{}\t{}\t{}\t{:?}\t{}\t{}\t{}\t{}\t{}\t{}",
                    row.stage,
                    row.current_command_failed,
                    row.target_exec_marker,
                    row.health_ready,
                    row.health_reason_stable,
                    row.binding_phase,
                    row.binding_registry_entries,
                    row.cgroup_removed,
                    row.cgroup_cleanup_count,
                    row.deferred_cleanup_count,
                    row.local_descriptors_released,
                    row.subsequent_bash_rejected,
                );
                assert!(row.current_command_failed, "{}", row.stage);
                assert!(!row.target_exec_marker, "{}", row.stage);
                assert!(!row.health_ready, "{}", row.stage);
                assert!(row.health_reason_stable, "{}", row.stage);
                assert_eq!(
                    row.binding_phase,
                    agent_runtime::runtime_fetch_proxy::CommandBindingPhase::Drained,
                    "{}",
                    row.stage
                );
                assert_eq!(row.binding_registry_entries, 0, "{}", row.stage);
                assert!(row.cgroup_removed, "{}", row.stage);
                assert_eq!(row.cgroup_cleanup_count, 1, "{}", row.stage);
                assert_eq!(row.deferred_cleanup_count, 0, "{}", row.stage);
                assert!(row.local_descriptors_released, "{}", row.stage);
                assert!(row.subsequent_bash_rejected, "{}", row.stage);
            }
            return;
        }
        #[cfg(not(feature = "c7-test-support"))]
        run_child(|| {
            close_range_for_fixture();
            let mut pipe = [-1; 2];
            assert_eq!(
                unsafe { libc::pipe2(pipe.as_mut_ptr(), libc::O_CLOEXEC) },
                0
            );
            assert_eq!(
                unsafe {
                    libc::write(
                        pipe[1],
                        CONFIG_SENTINEL.as_ptr().cast(),
                        CONFIG_SENTINEL.len(),
                    )
                },
                CONFIG_SENTINEL.len() as isize
            );
            unsafe {
                libc::close(pipe[1]);
            }
            let result = unsafe { install_exec_fds(pipe[0], 1_000_000, 1_000_001) };
            assert!(result.is_err());
            for fd in 3..64 {
                assert_fd_closed(fd);
            }
        });
    }

    #[tokio::test]
    async fn successful_target_exec_closes_status_fd_and_preserves_exit_one() {
        let fixture = tempfile::tempdir().unwrap();
        let cgroup_procs = fixture.path().join("cgroup.procs");
        std::fs::write(&cgroup_procs, b"").unwrap();
        let spec = ExecSpec {
            cgroup_procs,
            program: PathBuf::from("/bin/false"),
            args: Vec::new(),
            cwd: fixture.path().to_path_buf(),
            env: Vec::new(),
            rlimits: RlimitSpec::approved_defaults(),
        };

        let mut spawned = spawn_exec_helper(
            PathBuf::from(env!("CARGO_BIN_EXE_agent-runtime-exec")).as_path(),
            &spec,
        )
        .unwrap();
        assert_eq!(
            spawned
                .await_startup_status(Duration::from_secs(1))
                .await
                .unwrap(),
            ExecStartupOutcome::TargetExecSucceeded
        );
        let status = spawned.child.wait().await.unwrap();
        assert_eq!(status.code(), Some(1));
    }

    fn verify_layout((config_target, control_target, status_target): (i32, i32, i32)) {
        close_range_for_fixture();
        let mut config_pipe = [-1; 2];
        assert_eq!(
            unsafe { libc::pipe2(config_pipe.as_mut_ptr(), libc::O_CLOEXEC) },
            0
        );
        assert_eq!(
            unsafe {
                libc::write(
                    config_pipe[1],
                    CONFIG_SENTINEL.as_ptr().cast(),
                    CONFIG_SENTINEL.len(),
                )
            },
            CONFIG_SENTINEL.len() as isize
        );

        let mut control_pair = [-1; 2];
        assert_eq!(
            unsafe {
                libc::socketpair(
                    libc::AF_UNIX,
                    libc::SOCK_SEQPACKET | libc::SOCK_CLOEXEC,
                    0,
                    control_pair.as_mut_ptr(),
                )
            },
            0
        );
        assert_eq!(
            unsafe {
                libc::send(
                    control_pair[0],
                    CONTROL_NONCE.as_ptr().cast(),
                    CONTROL_NONCE.len(),
                    0,
                )
            },
            CONTROL_NONCE.len() as isize
        );

        let config_safe = unsafe { libc::fcntl(config_pipe[0], libc::F_DUPFD_CLOEXEC, 16) };
        let control_safe = unsafe { libc::fcntl(control_pair[1], libc::F_DUPFD_CLOEXEC, 16) };
        let mut status_pipe = [-1; 2];
        assert_eq!(
            unsafe { libc::pipe2(status_pipe.as_mut_ptr(), libc::O_CLOEXEC) },
            0
        );
        let status_peer = unsafe { libc::fcntl(status_pipe[0], libc::F_DUPFD_CLOEXEC, 64) };
        let status_safe = unsafe { libc::fcntl(status_pipe[1], libc::F_DUPFD_CLOEXEC, 16) };
        assert!(
            status_peer >= 64
                && config_safe >= 16
                && control_safe >= 16
                && status_safe >= 16
                && config_safe != control_safe
                && config_safe != status_safe
                && control_safe != status_safe
        );
        assert_eq!(unsafe { libc::close(status_pipe[0]) }, 0);
        assert_eq!(
            unsafe { libc::dup2(config_safe, config_target) },
            config_target
        );
        assert_eq!(
            unsafe { libc::dup2(control_safe, control_target) },
            control_target
        );
        assert_eq!(
            unsafe { libc::dup2(status_safe, status_target) },
            status_target
        );
        for fd in 3..64 {
            if fd != config_target && fd != control_target && fd != status_target {
                unsafe {
                    libc::close(fd);
                }
            }
        }

        unsafe { install_exec_fds(config_target, control_target, status_target) }.unwrap();
        let mut config = [0_u8; CONFIG_SENTINEL.len()];
        assert_eq!(
            unsafe { libc::read(EXEC_CONFIG_FD, config.as_mut_ptr().cast(), config.len(),) },
            config.len() as isize
        );
        assert_eq!(&config, CONFIG_SENTINEL);

        let mut nonce = [0_u8; CONTROL_NONCE.len()];
        assert_eq!(
            unsafe {
                libc::recv(
                    COMMAND_CONTROL_FD,
                    nonce.as_mut_ptr().cast(),
                    nonce.len(),
                    0,
                )
            },
            nonce.len() as isize
        );
        assert_eq!(&nonce, CONTROL_NONCE);
        let status_nonce = b"status-nonce";
        assert_eq!(
            unsafe {
                libc::write(
                    EXEC_STATUS_FD,
                    status_nonce.as_ptr().cast(),
                    status_nonce.len(),
                )
            },
            status_nonce.len() as isize
        );
        let mut observed_status = [0_u8; 12];
        assert_eq!(
            unsafe {
                libc::read(
                    status_peer,
                    observed_status.as_mut_ptr().cast(),
                    observed_status.len(),
                )
            },
            observed_status.len() as isize
        );
        assert_eq!(&observed_status, status_nonce);
        assert_eq!(unsafe { libc::close(status_peer) }, 0);
        for fd in [EXEC_CONFIG_FD, COMMAND_CONTROL_FD, EXEC_STATUS_FD] {
            let flags = unsafe { libc::fcntl(fd, libc::F_GETFD) };
            assert!(flags >= 0);
            assert_eq!(flags & libc::FD_CLOEXEC, 0);
        }
        for fd in 6..64 {
            assert_fd_closed(fd);
        }
    }

    fn close_range_for_fixture() {
        for fd in 3..64 {
            unsafe {
                libc::close(fd);
            }
        }
    }

    fn assert_fd_closed(fd: i32) {
        assert_eq!(unsafe { libc::fcntl(fd, libc::F_GETFD) }, -1);
        assert_eq!(io::Error::last_os_error().raw_os_error(), Some(libc::EBADF));
    }

    fn run_child(test: impl FnOnce() + Send + 'static) {
        let child = unsafe { libc::fork() };
        assert!(child >= 0, "fork failed: {}", io::Error::last_os_error());
        if child == 0 {
            let code = if catch_unwind(AssertUnwindSafe(test)).is_ok() {
                0
            } else {
                101
            };
            unsafe { libc::_exit(code) };
        }
        let mut status = MaybeUninit::<i32>::uninit();
        assert_eq!(
            unsafe { libc::waitpid(child, status.as_mut_ptr(), 0) },
            child
        );
        let status = unsafe { status.assume_init() };
        assert!(libc::WIFEXITED(status));
        assert_eq!(libc::WEXITSTATUS(status), 0);
    }
}

#[test]
fn c12_pre_exec_fd_mapper_source_is_child_safe() {
    const SPAWN: &str = include_str!("../src/exec/spawn.rs");
    const FD_MAP: &str = include_str!("../src/exec/spawn/fd_map.rs");
    const SYSCALLS: &str = include_str!("../src/exec/spawn/fd_map/syscalls.rs");
    const FAULTS: &str = include_str!("../src/exec/spawn/fd_map/c7_test_support.rs");
    const EXPECTED_CLOSURE: &str = r#"
        #[cfg(feature = "c7-test-support")]
        if let Some(fault) = fd_install_fault {
            return fd_map::c7_test_support::install_exec_fds_with_fault(
                inherited_fd,
                inherited_control_fd,
                inherited_status_fd,
                fault,
            );
        }
        install_exec_fds(inherited_fd, inherited_control_fd, inherited_status_fd)
    "#;
    const FORBIDDEN: &[&str] = &[
        "std::collections",
        "BTreeSet",
        "BTreeMap",
        "HashSet",
        "HashMap",
        "Vec<",
        "Vec::",
        "String",
        "Box<",
        "Box::",
        "CString",
        "OsString",
        "PathBuf",
        "Cow<",
        "alloc::",
        "format!",
        "format_args!",
        "write!",
        ".to_string(",
        "ToString",
        "Error::new",
        "Error::other",
        ".collect(",
        "collect::<",
        "std::sync",
        "sync::",
        "Mutex",
        "RwLock",
        "Condvar",
        "OnceLock",
        "LazyLock",
        "Arc<",
        "Arc::",
        "Rc<",
        "Rc::",
        "Atomic",
        ".lock(",
        "std::thread",
        "thread::",
        "panic!",
        "assert!",
        "debug_assert!",
        ".unwrap(",
        ".expect(",
        "println!",
        "eprintln!",
        "tracing::",
        "log::",
        "std::env",
        "env::",
        "std::fs",
        "fs::",
        "File::",
        "OpenOptions",
        "std::path",
        "path::",
    ];

    let helper = source_between(
        SPAWN,
        "fn spawn_exec_helper_with_control_inner(",
        "\n#[cfg(target_os = \"linux\")]\nfn kill_wait_spawned_helper_blocking",
    );
    let closure = source_between(helper, "command.pre_exec(move || {", "\n        });");
    assert_eq!(compact_source(closure), compact_source(EXPECTED_CLOSURE));

    let mut violations = Vec::new();
    for (module, source) in [
        ("fd_map", FD_MAP),
        ("fd_map/syscalls", SYSCALLS),
        ("fd_map/c7_test_support", FAULTS),
    ] {
        for &forbidden in FORBIDDEN {
            if source.contains(forbidden) {
                violations.push(format!("{module}: {forbidden}"));
            }
        }
    }
    assert!(violations.is_empty(), "{}", violations.join(", "));
}

fn source_between<'a>(source: &'a str, start: &str, end: &str) -> &'a str {
    let (_, tail) = source.split_once(start).expect("source start marker");
    let (section, _) = tail.split_once(end).expect("source end marker");
    section
}

fn compact_source(source: &str) -> String {
    source
        .chars()
        .filter(|character| !character.is_whitespace())
        .collect()
}

#[cfg(target_os = "linux")]
#[test]
fn c7_exec_fd_layouts_preserve_config_control_status() {
    linux_exec_helper::scenario_exec_fd_layouts_preserve_config_control_status();
}

#[cfg(target_os = "linux")]
#[tokio::test(flavor = "multi_thread")]
async fn c7_each_three_fd_mapping_stage_failure_aborts_and_latches() {
    #[cfg(feature = "c7-test-support")]
    agent_runtime::c7_test_support::enter_fd_exec_helper_if_requested();
    linux_exec_helper::scenario_each_three_fd_mapping_stage_failure_aborts_and_latches().await;
}

#[cfg(all(target_os = "linux", feature = "c7-test-support"))]
#[tokio::test(flavor = "multi_thread")]
async fn c7_config_writer_thread_creation_failure_latches_and_cleans() {
    let receipt = agent_runtime::c7_test_support::config_writer_thread_failure().await;
    assert!(receipt.current_command_failed);
    assert!(!receipt.helper_exec_marker);
    assert!(!receipt.health_ready);
    assert!(receipt.binding_drained);
    assert_eq!(receipt.binding_registry_entries, 0);
    assert!(receipt.cgroup_removed);
    assert_eq!(receipt.cgroup_cleanup_count, 1);
    assert_eq!(receipt.deferred_cleanup_count, 0);
    assert!(receipt.local_descriptors_released);
    assert!(receipt.subsequent_bash_rejected);
}
