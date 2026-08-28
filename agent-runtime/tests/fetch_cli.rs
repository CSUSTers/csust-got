use agent_runtime::fetch_cli::{
    BodySource, EXIT_AUTH, EXIT_NETWORK_PROTOCOL, EXIT_OUTPUT_IO, EXIT_STATUS, EXIT_USAGE,
    FetchCli, FetchExit, MAX_REQUEST_BODY_BYTES,
};
use std::path::PathBuf;

#[test]
fn parses_methods_headers_fields_and_body_modes() {
    for method in ["PATCH", "DELETE", "PROPFIND", "M-SEARCH"] {
        assert_eq!(
            FetchCli::parse(["fetch", method, "https://example.com"])
                .unwrap()
                .method
                .as_str(),
            method
        );
    }
    let json = FetchCli::parse([
        "fetch",
        "https://example.com",
        "Accept:text/plain",
        "name=alice",
        "count:=3",
    ])
    .unwrap();
    assert_eq!(json.method.as_str(), "POST");
    assert!(matches!(json.body, BodySource::Json(ref fields) if fields.len() == 2));
    assert_eq!(json.headers[0].0.as_str(), "accept");
    assert!(matches!(
        FetchCli::parse(["fetch", "--form", "https://example.com", "name=alice"])
            .unwrap()
            .body,
        BodySource::Form(ref fields) if fields.len() == 1
    ));
    assert!(matches!(
        FetchCli::parse(["fetch", "https://example.com", "name=alice", "file@a.bin"])
            .unwrap()
            .body,
        BodySource::Multipart(ref parts) if parts.len() == 2
    ));
    assert!(matches!(
        FetchCli::parse(["fetch", "POST", "https://example.com", "@-"])
            .unwrap()
            .body,
        BodySource::RawStdin
    ));
}

#[test]
fn header_values_preserve_equals_typed_and_upload_separators() {
    for (expression, expected_name, expected_value) in [
        (
            "Authorization:Bearer abc=def",
            "authorization",
            "Bearer abc=def",
        ),
        ("X-JSON:{\"op\":\"a:=b\"}", "x-json", "{\"op\":\"a:=b\"}"),
        ("X-File-Ref:value@name", "x-file-ref", "value@name"),
    ] {
        let cli = FetchCli::parse(["fetch", "https://example.com", expression]).unwrap();
        assert_eq!(cli.headers.len(), 1, "{expression}");
        assert_eq!(cli.headers[0].0.as_str(), expected_name, "{expression}");
        assert_eq!(cli.headers[0].1, expected_value, "{expression}");
        assert!(matches!(cli.body, BodySource::Empty), "{expression}");
    }
    let typed = FetchCli::parse(["fetch", "https://example.com", "count:=2"]).unwrap();
    assert!(matches!(
        typed.body,
        BodySource::Json(ref fields) if fields.len() == 1 && fields[0].value == 2
    ));
    let string = FetchCli::parse(["fetch", "https://example.com", "name=a=b"]).unwrap();
    assert!(matches!(
        string.body,
        BodySource::Json(ref fields) if fields.len() == 1 && fields[0].value == "a=b"
    ));
    let upload = FetchCli::parse(["fetch", "https://example.com", "file@/workspace/a@b"]).unwrap();
    assert!(matches!(
        upload.body,
        BodySource::Multipart(ref parts)
            if matches!(
                parts.as_slice(),
                [agent_runtime::fetch_cli::FormPart::File { name, path }]
                    if name == "file" && path == &PathBuf::from("/workspace/a@b")
            )
    ));
}

#[test]
fn rejects_unsafe_transport_body_and_timeout_arguments() {
    let cases: &[&[&str]] = &[
        &["fetch", "CONNECT", "https://example.com"],
        &["fetch", "https://example.com", "--proxy", "http://p"],
        &["fetch", "https://example.com", "--unix-socket", "/tmp/x"],
        &["fetch", "https://example.com", "--insecure"],
        &["fetch", "https://example.com", "Host:other.example"],
        &["fetch", "https://example.com", "Connection:upgrade"],
        &["fetch", "https://example.com", "Content-Length:4"],
        &["fetch", "https://example.com", "field=value", "--raw", "@-"],
        &["fetch", "https://example.com", "@-", "@payload.bin"],
        &["fetch", "https://example.com", "--timeout", "31s"],
        &["fetch", "https://example.com", "--timeout", "0"],
        &["fetch", "https://example.com", "--unknown"],
    ];
    for argv in cases {
        assert_eq!(
            FetchCli::parse(argv.iter().copied())
                .unwrap_err()
                .exit_code(),
            FetchExit::Usage,
            "accepted {argv:?}"
        );
    }
    let cli = FetchCli::parse([
        "fetch",
        "--follow",
        "--headers",
        "--check-status",
        "--timeout=2500ms",
        "--output",
        "out.bin",
        "https://example.com",
    ])
    .unwrap();
    assert!(cli.follow && cli.show_headers && cli.check_status);
    assert_eq!(cli.timeout.unwrap().as_millis(), 2_500);
    assert_eq!(cli.output.unwrap(), PathBuf::from("out.bin"));
}

#[test]
fn exit_codes_and_budget_are_stable_public_contract() {
    assert_eq!(FetchExit::Success as i32, 0);
    assert_eq!(FetchExit::Usage as i32, 2);
    assert_eq!(FetchExit::HttpStatus as i32, 22);
    assert_eq!(FetchExit::Timeout as i32, 28);
    assert_eq!(FetchExit::Policy as i32, 65);
    assert_eq!(FetchExit::Unavailable as i32, 69);
    assert_eq!(FetchExit::Internal as i32, 70);
    assert_eq!((EXIT_USAGE, EXIT_STATUS, EXIT_AUTH), (2, 22, 69));
    assert_eq!((EXIT_NETWORK_PROTOCOL, EXIT_OUTPUT_IO), (69, 70));
    assert_eq!(MAX_REQUEST_BODY_BYTES, 8 * 1024 * 1024);
}

#[cfg(all(target_os = "linux", feature = "c7-test-support"))]
#[test]
fn c7_output_policy_and_internal_terminal_errors_map_exact_exit_codes() {
    use agent_runtime::fetch_protocol::ErrorCode;

    for (code, exit) in [
        (ErrorCode::Policy, 65),
        (ErrorCode::Internal, 70),
        (ErrorCode::Timeout, 28),
        (ErrorCode::Auth, 69),
        (ErrorCode::Network, 69),
        (ErrorCode::Protocol, 69),
    ] {
        assert_eq!(
            agent_runtime::c7_test_support::exit_for_error_code(code),
            exit
        );
    }
}

#[cfg(target_os = "linux")]
fn linux_cli_fixture_lock() -> std::fs::File {
    use fs2::FileExt as _;

    let executable = std::fs::File::open(env!("CARGO_BIN_EXE_fetch")).unwrap();
    executable.lock_exclusive().unwrap();
    executable
}

#[cfg(target_os = "linux")]
#[test]
fn workspace_inputs_are_rooted_at_literal_workspace_for_every_cwd() {
    use std::os::unix::fs::symlink;

    let _fixture_lock = linux_cli_fixture_lock();
    let workspace = std::path::Path::new("/workspace");
    assert!(workspace.is_dir(), "test requires literal /workspace");
    let suffix = format!("fetch-c3-{}", std::process::id());
    let name = format!(".{suffix}.txt");
    let input = workspace.join(&name);
    let directory = workspace.join(format!(".{suffix}.dir"));
    let file_link = workspace.join(format!(".{suffix}.link"));
    let parent_link = workspace.join(format!(".{suffix}.parent"));
    std::fs::write(&input, b"workspace input").unwrap();
    std::fs::create_dir(&directory).unwrap();
    symlink(&input, &file_link).unwrap();
    symlink(&directory, &parent_link).unwrap();
    let run = |cwd: &std::path::Path, raw: &str| {
        std::process::Command::new(env!("CARGO_BIN_EXE_fetch"))
            .args(["POST", "https://example.com", "--raw", raw])
            .current_dir(cwd)
            .env_clear()
            .output()
            .unwrap()
            .status
            .code()
    };
    let relative = format!("@{name}");
    let absolute = format!("@{}", input.display());
    let rooted = [
        std::path::Path::new("/"),
        std::path::Path::new("/tmp"),
        workspace,
        directory.as_path(),
    ]
    .map(|cwd| (cwd.to_path_buf(), run(cwd, &relative), run(cwd, &absolute)));
    let rejected = [
        "@/workspace".to_string(),
        "@/skills/secret".to_string(),
        "@../escape".to_string(),
        "@/workspace-prefix/file".to_string(),
        format!("@{}", file_link.display()),
        format!("@{}/missing", parent_link.display()),
        format!("@{}", directory.display()),
    ]
    .map(|raw| {
        let status = run(workspace, &raw);
        (raw, status)
    });
    std::fs::remove_file(&file_link).unwrap();
    std::fs::remove_file(&parent_link).unwrap();
    std::fs::remove_dir(&directory).unwrap();
    std::fs::remove_file(&input).unwrap();
    for (cwd, relative_status, absolute_status) in rooted {
        assert_eq!(
            relative_status,
            Some(69),
            "relative path for {}",
            cwd.display()
        );
        assert_eq!(
            absolute_status,
            Some(69),
            "absolute path for {}",
            cwd.display()
        );
    }
    for (raw, status) in rejected {
        assert_eq!(status, Some(65), "accepted {raw}");
    }
}

#[cfg(not(target_os = "linux"))]
#[test]
fn non_linux_binary_fails_closed() {
    let output = std::process::Command::new(env!("CARGO_BIN_EXE_fetch"))
        .output()
        .unwrap();
    assert_eq!(output.status.code(), Some(69));
    assert!(String::from_utf8_lossy(&output.stderr).contains("unavailable"));
}

#[cfg(target_os = "linux")]
mod linux_cli {
    use agent_runtime::{
        fetch_cli::MAX_REQUEST_BODY_BYTES,
        fetch_protocol::{
            ErrorCode, FETCH_PROTOCOL_VERSION, FetchProtocolErrorFrame, FetchResponseHead,
            LocalClientFrame, LocalResponseEnd, LocalRuntimeFrame, MAX_METADATA_BYTES,
            read_local_client_frame, write_local_client_frame, write_local_runtime_frame,
        },
        runtime_fetch_proxy::{COMMAND_CONTROL_CANCEL_GRACE, CommandControlPacket},
    };
    use std::{
        io,
        mem::{MaybeUninit, size_of},
        os::fd::{AsRawFd as _, FromRawFd as _, IntoRawFd as _, OwnedFd},
        process::Stdio,
    };
    use tokio::{
        io::{AsyncReadExt as _, AsyncWriteExt as _},
        process::Command,
    };

    const TEST_REQUEST_TIMEOUT: std::time::Duration = std::time::Duration::from_millis(50);
    const TEST_COMPLETION_BOUND: std::time::Duration = std::time::Duration::from_millis(200);

    struct ControlPair {
        _fixture_lock: std::fs::File,
        runtime: OwnedFd,
        child: OwnedFd,
    }

    struct Session {
        packet: CommandControlPacket,
        stream: tokio::net::UnixStream,
        inode: u64,
    }

    struct CapturedBody {
        bytes: Vec<u8>,
        chunks: usize,
    }

    #[derive(Debug)]
    enum TimeoutTerminal {
        Cancel,
        EofAtFrameBoundary,
        EofDuringBody,
    }

    struct TimedBody {
        cancellation_elapsed: std::time::Duration,
        setup_elapsed: std::time::Duration,
        terminal: Option<TimeoutTerminal>,
    }

    impl ControlPair {
        fn new() -> Self {
            let fixture_lock = super::linux_cli_fixture_lock();
            let mut sockets = [-1_i32; 2];
            assert_eq!(
                unsafe {
                    libc::socketpair(
                        libc::AF_UNIX,
                        libc::SOCK_SEQPACKET | libc::SOCK_CLOEXEC,
                        0,
                        sockets.as_mut_ptr(),
                    )
                },
                0,
                "{}",
                io::Error::last_os_error()
            );
            Self {
                _fixture_lock: fixture_lock,
                runtime: unsafe { OwnedFd::from_raw_fd(sockets[0]) },
                child: unsafe { OwnedFd::from_raw_fd(sockets[1]) },
            }
        }

        fn command(&self, args: &[&str]) -> Command {
            let mut command = Command::new(env!("CARGO_BIN_EXE_fetch"));
            command
                .args(args)
                .current_dir("/workspace")
                .env_clear()
                .env("AGENT_FETCH_CONTROL_FD", "4")
                .stdin(Stdio::null())
                .stdout(Stdio::piped())
                .stderr(Stdio::piped());
            self.inherit(&mut command);
            command
        }

        fn inherit(&self, command: &mut Command) {
            let duplicate =
                unsafe { libc::fcntl(self.child.as_raw_fd(), libc::F_DUPFD_CLOEXEC, 10) };
            assert!(duplicate >= 10, "{}", io::Error::last_os_error());
            let inherited = unsafe { OwnedFd::from_raw_fd(duplicate) };
            unsafe {
                command.pre_exec(move || {
                    if libc::dup2(inherited.as_raw_fd(), 4) == -1 {
                        return Err(io::Error::last_os_error());
                    }
                    if libc::fcntl(4, libc::F_SETFD, 0) == -1 {
                        return Err(io::Error::last_os_error());
                    }
                    Ok(())
                });
            }
        }

        fn receive(&self) -> io::Result<Session> {
            let mut ready = libc::pollfd {
                fd: self.runtime.as_raw_fd(),
                events: libc::POLLIN,
                revents: 0,
            };
            match unsafe { libc::poll(&mut ready, 1, 300) } {
                0 => {
                    return Err(io::Error::new(
                        io::ErrorKind::TimedOut,
                        "no session transfer",
                    ));
                }
                value if value < 0 => return Err(io::Error::last_os_error()),
                _ => {}
            }
            receive_session(self.runtime.as_raw_fd())
        }
    }

    fn receive_session(control: i32) -> io::Result<Session> {
        let mut payload = vec![0_u8; MAX_METADATA_BYTES];
        let mut ancillary = [MaybeUninit::<libc::cmsghdr>::uninit(); 16];
        let mut iovec = libc::iovec {
            iov_base: payload.as_mut_ptr().cast(),
            iov_len: payload.len(),
        };
        let mut message: libc::msghdr = unsafe { std::mem::zeroed() };
        message.msg_iov = &mut iovec;
        message.msg_iovlen = 1;
        message.msg_control = ancillary.as_mut_ptr().cast();
        message.msg_controllen = size_of_val(&ancillary);
        let received = unsafe { libc::recvmsg(control, &mut message, libc::MSG_CMSG_CLOEXEC) };
        if received <= 0 || message.msg_flags & (libc::MSG_TRUNC | libc::MSG_CTRUNC) != 0 {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "bad control packet",
            ));
        }
        let header = unsafe { libc::CMSG_FIRSTHDR(&message) };
        if header.is_null()
            || unsafe { (*header).cmsg_level } != libc::SOL_SOCKET
            || unsafe { (*header).cmsg_type } != libc::SCM_RIGHTS
            || unsafe { (*header).cmsg_len }
                != unsafe { libc::CMSG_LEN(size_of::<i32>() as u32) as usize }
            || !unsafe { libc::CMSG_NXTHDR(&message, header) }.is_null()
        {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "descriptor count",
            ));
        }
        let descriptor = unsafe { OwnedFd::from_raw_fd(*libc::CMSG_DATA(header).cast::<i32>()) };
        let mut stats = MaybeUninit::<libc::stat>::uninit();
        if unsafe { libc::fstat(descriptor.as_raw_fd(), stats.as_mut_ptr()) } != 0 {
            return Err(io::Error::last_os_error());
        }
        let inode = unsafe { stats.assume_init() }.st_ino;
        let packet = serde_json::from_slice(&payload[..received as usize])
            .map_err(|error| io::Error::new(io::ErrorKind::InvalidData, error))?;
        let standard =
            unsafe { std::os::unix::net::UnixStream::from_raw_fd(descriptor.into_raw_fd()) };
        standard.set_nonblocking(true)?;
        Ok(Session {
            packet,
            stream: tokio::net::UnixStream::from_std(standard)?,
            inode,
        })
    }

    async fn body(session: &mut Session) -> CapturedBody {
        write_local_runtime_frame(&mut session.stream, &LocalRuntimeFrame::Continue)
            .await
            .unwrap();
        let mut bytes = Vec::new();
        let mut chunks = 0;
        loop {
            match read_local_client_frame(&mut session.stream).await.unwrap() {
                LocalClientFrame::BodyChunk(chunk) => {
                    chunks += 1;
                    bytes.extend_from_slice(&chunk);
                }
                LocalClientFrame::BodyEnd => return CapturedBody { bytes, chunks },
                frame => panic!("unexpected request frame: {frame:?}"),
            }
        }
    }

    async fn response(
        session: &mut Session,
        status: u16,
        headers: Vec<(String, String)>,
        bytes: &[u8],
        output: bool,
    ) {
        write_local_runtime_frame(
            &mut session.stream,
            &LocalRuntimeFrame::ResponseHead(FetchResponseHead {
                protocol_version: FETCH_PROTOCOL_VERSION,
                status,
                reason: if status >= 400 { "Not Found" } else { "OK" }.to_string(),
                headers,
            }),
        )
        .await
        .unwrap();
        if !output && !bytes.is_empty() {
            write_local_runtime_frame(
                &mut session.stream,
                &LocalRuntimeFrame::ResponseChunk(bytes::Bytes::copy_from_slice(bytes)),
            )
            .await
            .unwrap();
        }
        write_local_runtime_frame(
            &mut session.stream,
            &LocalRuntimeFrame::ResponseEnd(LocalResponseEnd {
                protocol_version: FETCH_PROTOCOL_VERSION,
                body_bytes: bytes.len() as u64,
                output_committed: output,
            }),
        )
        .await
        .unwrap();
    }

    async fn complete(mut session: Session, bytes: &[u8], output: bool) {
        body(&mut session).await;
        response(&mut session, 200, Vec::new(), bytes, output).await;
    }

    async fn output_response(
        session: &mut Session,
        status: u16,
        bytes: &[u8],
        output_committed: bool,
    ) {
        write_local_runtime_frame(
            &mut session.stream,
            &LocalRuntimeFrame::ResponseHead(FetchResponseHead {
                protocol_version: FETCH_PROTOCOL_VERSION,
                status,
                reason: if status >= 400 { "Not Found" } else { "OK" }.to_string(),
                headers: Vec::new(),
            }),
        )
        .await
        .unwrap();
        write_local_runtime_frame(
            &mut session.stream,
            &LocalRuntimeFrame::ResponseEnd(LocalResponseEnd {
                protocol_version: FETCH_PROTOCOL_VERSION,
                body_bytes: bytes.len() as u64,
                output_committed,
            }),
        )
        .await
        .unwrap();
    }

    #[tokio::test]
    async fn fetch_reads_only_the_fixed_control_fd() {
        let pair = ControlPair::new();
        let child = pair
            .command(&["https://example.com/fixed-control"])
            .spawn()
            .unwrap();
        let session = pair.receive().unwrap();
        assert_eq!(
            session.packet.request.url,
            "https://example.com/fixed-control"
        );
        complete(session, b"fd4-only", false).await;
        let output = child.wait_with_output().await.unwrap();
        assert!(
            output.status.success(),
            "{}",
            String::from_utf8_lossy(&output.stderr)
        );
        assert_eq!(output.stdout, b"fd4-only");
    }

    #[tokio::test]
    async fn each_invocation_transfers_exactly_one_fresh_session_endpoint() {
        let pair = ControlPair::new();
        let mut inodes = Vec::new();
        for expected in [b"first".as_slice(), b"second".as_slice()] {
            let child = pair.command(&["https://example.com"]).spawn().unwrap();
            let session = pair.receive().unwrap();
            inodes.push(session.inode);
            complete(session, expected, false).await;
            assert_eq!(child.wait_with_output().await.unwrap().stdout, expected);
        }
        assert_ne!(inodes[0], inodes[1]);
    }

    #[tokio::test]
    async fn output_path_is_metadata_and_cli_never_opens_destination() {
        let pair = ControlPair::new();
        let name = format!(".fetch-output-must-not-open-{}", std::process::id());
        let destination = std::path::Path::new("/workspace").join(&name);
        let relative = format!("./{name}");
        std::fs::create_dir(&destination).unwrap();
        let child = pair
            .command(&["--output", &relative, "https://example.com/output"])
            .spawn()
            .unwrap();
        let session = pair.receive().unwrap();
        assert_eq!(session.packet.output_path.as_deref(), destination.to_str());
        complete(session, b"runtime-owned", true).await;
        let output = child.wait_with_output().await.unwrap();
        let untouched = destination.is_dir();
        std::fs::remove_dir(&destination).unwrap();
        assert!(
            output.status.success(),
            "{}",
            String::from_utf8_lossy(&output.stderr)
        );
        assert!(output.stdout.is_empty());
        assert!(untouched);
    }

    #[tokio::test]
    async fn check_status_output_uncommitted_returns_22_and_preserves_destination() {
        let pair = ControlPair::new();
        let name = format!(".fetch-check-status-output-{}", std::process::id());
        let destination = std::path::Path::new("/workspace").join(&name);
        std::fs::write(&destination, b"old-response").unwrap();
        let output_path = format!("/workspace/{name}");
        let child = pair
            .command(&[
                "--check-status",
                "--output",
                &output_path,
                "https://example.com/missing",
            ])
            .spawn()
            .unwrap();
        let mut session = pair.receive().unwrap();
        assert!(session.packet.request.check_status);
        assert_eq!(
            session.packet.output_path.as_deref(),
            Some(output_path.as_str())
        );
        body(&mut session).await;
        output_response(&mut session, 404, b"new-response", false).await;

        let output = tokio::time::timeout(
            std::time::Duration::from_millis(500),
            child.wait_with_output(),
        )
        .await
        .expect("fetch did not return status exit promptly")
        .unwrap();
        let preserved = std::fs::read(&destination).unwrap();
        std::fs::remove_file(destination).unwrap();
        assert_eq!(output.status.code(), Some(22));
        assert!(output.stdout.is_empty());
        assert_eq!(preserved, b"old-response");
        assert_eq!(
            String::from_utf8(output.stderr).unwrap(),
            "fetch: HTTP status 404 Not Found\n"
        );
    }

    #[tokio::test]
    async fn output_commit_result_mismatches_fail_closed() {
        let cases: &[(&str, &[&str], u16, bool)] = &[
            (
                "successful output was not committed",
                &[
                    "--check-status",
                    "--output",
                    "/workspace/.fetch-mismatch-success",
                    "https://example.com/success",
                ],
                200,
                false,
            ),
            (
                "unchecked error output was not committed",
                &[
                    "--output",
                    "/workspace/.fetch-mismatch-unchecked",
                    "https://example.com/missing",
                ],
                404,
                false,
            ),
            (
                "stdout response claimed an output commit",
                &["https://example.com/stdout"],
                200,
                true,
            ),
            (
                "checked error output was committed",
                &[
                    "--check-status",
                    "--output",
                    "/workspace/.fetch-mismatch-checked",
                    "https://example.com/missing",
                ],
                404,
                true,
            ),
        ];
        for (name, args, status, output_committed) in cases {
            let pair = ControlPair::new();
            let child = pair.command(args).spawn().unwrap();
            let mut session = pair.receive().unwrap();
            body(&mut session).await;
            output_response(&mut session, *status, b"", *output_committed).await;
            let output = child.wait_with_output().await.unwrap();
            assert_eq!(output.status.code(), Some(69), "{name}");
            assert!(output.stdout.is_empty(), "{name}");
            assert!(
                String::from_utf8_lossy(&output.stderr)
                    .contains("runtime output commit result does not match request metadata"),
                "{name}: {}",
                String::from_utf8_lossy(&output.stderr)
            );
        }
    }

    #[tokio::test]
    async fn headers_status_and_structured_bodies_preserve_stream_contracts() {
        let pair = ControlPair::new();
        let child = pair
            .command(&[
                "--headers",
                "--check-status",
                "https://example.com",
                "count:=2",
            ])
            .spawn()
            .unwrap();
        let mut session = pair.receive().unwrap();
        assert!(
            session
                .packet
                .request
                .headers
                .iter()
                .any(|(name, value)| name == "content-type" && value == "application/json")
        );
        assert_eq!(body(&mut session).await.bytes, br#"{"count":2}"#);
        response(
            &mut session,
            404,
            vec![("x-result".to_string(), "missing".to_string())],
            b"response-body",
            false,
        )
        .await;
        let output = child.wait_with_output().await.unwrap();
        assert_eq!(output.status.code(), Some(22));
        assert_eq!(output.stdout, b"response-body");
        let stderr = String::from_utf8(output.stderr).unwrap();
        assert!(stderr.contains("HTTP 404 Not Found"));
        assert!(stderr.contains("x-result: missing"));
        assert!(!stderr.contains("response-body"));
    }

    #[tokio::test]
    async fn raw_workspace_inputs_stream_in_bounded_chunks() {
        let pair = ControlPair::new();
        let name = format!(".fetch-large-input-{}", std::process::id());
        let path = std::path::Path::new("/workspace").join(&name);
        std::fs::write(&path, vec![b'f'; 150_000]).unwrap();
        let raw = format!("@{name}");
        let child = pair
            .command(&["POST", "https://example.com", "--raw", &raw])
            .spawn()
            .unwrap();
        let mut session = pair.receive().unwrap();
        let captured = body(&mut session).await;
        response(&mut session, 200, Vec::new(), b"ok", false).await;
        let output = child.wait_with_output().await.unwrap();
        std::fs::remove_file(path).unwrap();
        assert!(
            output.status.success(),
            "{}",
            String::from_utf8_lossy(&output.stderr)
        );
        assert_eq!(captured.bytes, vec![b'f'; 150_000]);
        assert!(captured.chunks > 1);
    }

    fn shrink_receive_buffer(stream: &tokio::net::UnixStream) {
        let bytes = 4_096_i32;
        assert_eq!(
            unsafe {
                libc::setsockopt(
                    stream.as_raw_fd(),
                    libc::SOL_SOCKET,
                    libc::SO_RCVBUF,
                    (&bytes as *const i32).cast(),
                    size_of::<i32>() as libc::socklen_t,
                )
            },
            0
        );
    }

    async fn encoded_cancel() -> Vec<u8> {
        let (mut writer, mut reader) = tokio::io::duplex(16);
        write_local_client_frame(&mut writer, &LocalClientFrame::Cancel)
            .await
            .unwrap();
        writer.shutdown().await.unwrap();
        let mut encoded = Vec::new();
        reader.read_to_end(&mut encoded).await.unwrap();
        encoded
    }

    fn timeout_terminal(wire: &[u8], cancel: &[u8]) -> (usize, TimeoutTerminal) {
        let mut cursor = 0;
        let mut chunks = 0;
        loop {
            let remaining = &wire[cursor..];
            if remaining.is_empty() {
                return (chunks, TimeoutTerminal::EofAtFrameBoundary);
            }
            if remaining.starts_with(cancel) {
                assert_eq!(remaining, cancel, "bytes followed the terminal Cancel");
                return (chunks, TimeoutTerminal::Cancel);
            }
            if remaining.len() < 5 {
                assert!(!remaining.windows(cancel.len()).any(|bytes| bytes == cancel));
                return (chunks, TimeoutTerminal::EofDuringBody);
            }
            let payload_len = u32::from_be_bytes(remaining[1..5].try_into().unwrap()) as usize;
            assert!(payload_len > 0, "body completed before timeout");
            let frame_len = 5 + payload_len;
            let available_payload = &remaining[5..remaining.len().min(frame_len)];
            assert!(
                !available_payload
                    .windows(cancel.len())
                    .any(|bytes| bytes == cancel),
                "Cancel was interleaved into an interrupted BodyChunk"
            );
            assert!(available_payload.iter().all(|byte| *byte == b'x'));
            if remaining.len() < frame_len {
                return (chunks, TimeoutTerminal::EofDuringBody);
            }
            chunks += 1;
            cursor += frame_len;
        }
    }

    async fn timed_body(pair: &ControlPair, read_delay: Option<std::time::Duration>) -> TimedBody {
        let started = tokio::time::Instant::now();
        let mut command = pair.command(&[
            "--timeout=50ms",
            "POST",
            "https://example.com/timeout",
            "--raw",
            "@-",
        ]);
        command.stdin(Stdio::piped());
        let mut child = command.spawn().unwrap();
        let mut input = child.stdin.take().unwrap();
        let writing = tokio::spawn(async move {
            let _ = input.write_all(&vec![b'x'; MAX_REQUEST_BODY_BYTES]).await;
        });
        let mut session = pair.receive().unwrap();
        shrink_receive_buffer(&session.stream);
        write_local_runtime_frame(&mut session.stream, &LocalRuntimeFrame::Continue)
            .await
            .unwrap();
        let cancellation_started = tokio::time::Instant::now();
        let setup_elapsed = cancellation_started.duration_since(started);
        let waiting = tokio::spawn(async move {
            let output = tokio::time::timeout(TEST_COMPLETION_BOUND, child.wait_with_output())
                .await
                .expect("fetch exceeded the timeout/cancel bound")
                .unwrap();
            (output, cancellation_started.elapsed())
        });
        let terminal = if let Some(read_delay) = read_delay {
            tokio::time::sleep(read_delay).await;
            let mut wire = Vec::new();
            tokio::time::timeout(
                std::time::Duration::from_millis(100),
                session.stream.read_to_end(&mut wire),
            )
            .await
            .expect("session was not terminated within the cancellation bound")
            .unwrap();
            let (chunks, terminal) = timeout_terminal(&wire, &encoded_cancel().await);
            assert!(chunks > 0);
            Some(terminal)
        } else {
            None
        };
        let (output, completed) = waiting.await.unwrap();
        writing.await.unwrap();
        assert_eq!(output.status.code(), Some(28));
        if read_delay.is_none() {
            let mut closed = libc::pollfd {
                fd: session.stream.as_raw_fd(),
                events: libc::POLLRDHUP,
                revents: 0,
            };
            assert!(unsafe { libc::poll(&mut closed, 1, 20) } > 0);
            assert!(closed.revents & libc::POLLRDHUP != 0);
        }
        TimedBody {
            cancellation_elapsed: completed,
            setup_elapsed,
            terminal,
        }
    }

    #[tokio::test]
    async fn timeout_drops_inflight_future_before_cancel() {
        let timing = timed_body(
            &ControlPair::new(),
            Some(TEST_REQUEST_TIMEOUT + std::time::Duration::from_millis(20)),
        )
        .await;
        assert!(timing.terminal.is_some());
        assert!(
            timing.cancellation_elapsed <= TEST_COMPLETION_BOUND,
            "cancellation completed in {:?} after {:?} of setup",
            timing.cancellation_elapsed,
            timing.setup_elapsed
        );
    }

    #[tokio::test]
    async fn cancel_is_bounded_when_peer_never_reads() {
        let timing = timed_body(&ControlPair::new(), None).await;
        assert!(
            timing.cancellation_elapsed <= TEST_COMPLETION_BOUND,
            "cancellation completed in {:?} after {:?} of setup",
            timing.cancellation_elapsed,
            timing.setup_elapsed
        );
    }

    #[tokio::test]
    async fn late_peer_read_observes_bounded_shutdown_without_malformed_cancel() {
        let timing = timed_body(
            &ControlPair::new(),
            Some(
                TEST_REQUEST_TIMEOUT
                    + COMMAND_CONTROL_CANCEL_GRACE
                    + std::time::Duration::from_millis(20),
            ),
        )
        .await;
        assert!(matches!(
            timing.terminal,
            Some(TimeoutTerminal::EofAtFrameBoundary | TimeoutTerminal::EofDuringBody)
        ));
        assert!(
            timing.cancellation_elapsed <= TEST_COMPLETION_BOUND,
            "cancellation completed in {:?} after {:?} of setup",
            timing.cancellation_elapsed,
            timing.setup_elapsed
        );
    }

    #[tokio::test]
    async fn oversized_stdin_stops_at_budget_and_sends_cancel() {
        let pair = ControlPair::new();
        let mut command = pair.command(&["POST", "https://example.com", "@-"]);
        command.stdin(Stdio::piped());
        let mut child = command.spawn().unwrap();
        let mut input = child.stdin.take().unwrap();
        let writing = tokio::spawn(async move {
            let _ = input
                .write_all(&vec![b'x'; MAX_REQUEST_BODY_BYTES + 1])
                .await;
        });
        let mut session = pair.receive().unwrap();
        write_local_runtime_frame(&mut session.stream, &LocalRuntimeFrame::Continue)
            .await
            .unwrap();
        let mut received = 0;
        loop {
            match read_local_client_frame(&mut session.stream).await.unwrap() {
                LocalClientFrame::BodyChunk(chunk) => received += chunk.len(),
                LocalClientFrame::Cancel => break,
                frame => panic!("unexpected frame: {frame:?}"),
            }
        }
        let output = child.wait_with_output().await.unwrap();
        writing.await.unwrap();
        assert_eq!(received, MAX_REQUEST_BODY_BYTES);
        assert_eq!(output.status.code(), Some(65));
    }

    #[tokio::test]
    async fn runtime_errors_and_truncation_fail_closed() {
        let pair = ControlPair::new();
        let denied = pair.command(&["https://example.com"]).spawn().unwrap();
        let mut denied_session = pair.receive().unwrap();
        write_local_runtime_frame(
            &mut denied_session.stream,
            &LocalRuntimeFrame::Error(FetchProtocolErrorFrame {
                protocol_version: FETCH_PROTOCOL_VERSION,
                code: ErrorCode::Policy,
                message: "denied".to_string(),
            }),
        )
        .await
        .unwrap();
        assert_eq!(
            read_local_client_frame(&mut denied_session.stream)
                .await
                .unwrap(),
            LocalClientFrame::Cancel
        );
        assert_eq!(
            denied.wait_with_output().await.unwrap().status.code(),
            Some(65)
        );

        let output_error = pair.command(&["https://example.com"]).spawn().unwrap();
        let mut output_session = pair.receive().unwrap();
        body(&mut output_session).await;
        write_local_runtime_frame(
            &mut output_session.stream,
            &LocalRuntimeFrame::Error(FetchProtocolErrorFrame {
                protocol_version: FETCH_PROTOCOL_VERSION,
                code: ErrorCode::Internal,
                message: "output failed".to_string(),
            }),
        )
        .await
        .unwrap();
        assert_eq!(
            read_local_client_frame(&mut output_session.stream)
                .await
                .unwrap(),
            LocalClientFrame::Cancel
        );
        assert_eq!(
            output_error.wait_with_output().await.unwrap().status.code(),
            Some(70)
        );

        let truncated = pair.command(&["https://example.com"]).spawn().unwrap();
        let mut session = pair.receive().unwrap();
        body(&mut session).await;
        write_local_runtime_frame(
            &mut session.stream,
            &LocalRuntimeFrame::ResponseHead(FetchResponseHead {
                protocol_version: FETCH_PROTOCOL_VERSION,
                status: 200,
                reason: "OK".to_string(),
                headers: Vec::new(),
            }),
        )
        .await
        .unwrap();
        drop(session);
        assert_eq!(
            truncated.wait_with_output().await.unwrap().status.code(),
            Some(69)
        );
    }

    #[tokio::test]
    async fn ctrl_c_sends_cancel_and_exits_without_diagnostic() {
        let pair = ControlPair::new();
        let child = pair.command(&["https://example.com"]).spawn().unwrap();
        let pid = child.id().unwrap();
        let mut session = pair.receive().unwrap();
        body(&mut session).await;
        assert_eq!(unsafe { libc::kill(pid as libc::pid_t, libc::SIGINT) }, 0);
        assert_eq!(
            tokio::time::timeout(
                std::time::Duration::from_millis(200),
                read_local_client_frame(&mut session.stream),
            )
            .await
            .unwrap()
            .unwrap(),
            LocalClientFrame::Cancel
        );
        let output = child.wait_with_output().await.unwrap();
        assert!(output.status.success());
        assert!(output.stderr.is_empty());
    }

    #[tokio::test]
    async fn broken_pipe_does_not_emit_a_second_diagnostic() {
        let pair = ControlPair::new();
        let script = format!(
            "\"{}\" https://example.com | head -c 1 >/dev/null",
            env!("CARGO_BIN_EXE_fetch")
        );
        let mut command = Command::new("/bin/bash");
        command
            .args(["-c", &script])
            .current_dir("/workspace")
            .env_clear()
            .env("AGENT_FETCH_CONTROL_FD", "4")
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        pair.inherit(&mut command);
        let child = command.spawn().unwrap();
        let mut session = pair.receive().unwrap();
        body(&mut session).await;
        write_local_runtime_frame(
            &mut session.stream,
            &LocalRuntimeFrame::ResponseHead(FetchResponseHead {
                protocol_version: FETCH_PROTOCOL_VERSION,
                status: 200,
                reason: "OK".to_string(),
                headers: Vec::new(),
            }),
        )
        .await
        .unwrap();
        let (mut reader, mut writer) = session.stream.into_split();
        let writing = tokio::spawn(async move {
            let chunk = bytes::Bytes::from(vec![b'x'; 64 * 1024]);
            loop {
                if write_local_runtime_frame(
                    &mut writer,
                    &LocalRuntimeFrame::ResponseChunk(chunk.clone()),
                )
                .await
                .is_err()
                {
                    break;
                }
            }
        });
        assert_eq!(
            tokio::time::timeout(
                std::time::Duration::from_secs(2),
                read_local_client_frame(&mut reader),
            )
            .await
            .unwrap()
            .unwrap(),
            LocalClientFrame::Cancel
        );
        writing.abort();
        let output = child.wait_with_output().await.unwrap();
        assert!(output.status.success());
        assert!(
            output.stderr.is_empty(),
            "{}",
            String::from_utf8_lossy(&output.stderr)
        );
    }
}
