#[cfg(target_os = "linux")]
fn main() -> anyhow::Result<()> {
    use agent_runtime::exec::{EXEC_CONFIG_FD, EXEC_CONFIG_FLAG};

    let mut args = std::env::args_os().skip(1);
    if args.next().as_deref() != Some(std::ffi::OsStr::new(EXEC_CONFIG_FLAG)) {
        anyhow::bail!("usage: agent-runtime-exec --config-fd 3");
    }
    let fd = args
        .next()
        .ok_or_else(|| anyhow::anyhow!("usage: agent-runtime-exec --config-fd 3"))?;
    let expected_fd = std::ffi::OsString::from(EXEC_CONFIG_FD.to_string());
    if args.next().is_some() || fd != expected_fd {
        anyhow::bail!("usage: agent-runtime-exec --config-fd 3");
    }
    match agent_runtime::exec::exec_from_config_fd(EXEC_CONFIG_FD) {
        Ok(never) => match never {},
        Err(_) => std::process::exit(78),
    }
}

#[cfg(not(target_os = "linux"))]
fn main() -> anyhow::Result<()> {
    anyhow::bail!("agent runtime production execution requires Linux")
}
