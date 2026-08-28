use super::super::supervisor::{CommandHandle, CommandSupervisor};
use super::super::*;

impl CommandSupervisor {
    pub fn start(
        &self,
        target: ExecTarget,
        env: Vec<(String, String)>,
        timeout: Duration,
    ) -> Result<CommandHandle, SupervisorError> {
        let sequence = self.inner.sequence.fetch_add(1, Ordering::Relaxed);
        let command_id = format!(
            "{}-{}-{sequence}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap_or_default()
                .as_nanos()
        );
        self.start_command(
            CommandIdentity::new("runtime", "command", &command_id),
            target,
            env,
            timeout,
            12_000,
            None,
        )
    }

    pub fn start_command(
        &self,
        identity: CommandIdentity,
        target: ExecTarget,
        env: Vec<(String, String)>,
        timeout: Duration,
        max_output_chars: usize,
        cleanup_dir: Option<PathBuf>,
    ) -> Result<CommandHandle, SupervisorError> {
        self.start_command_with_environment(
            identity,
            target,
            move || Ok(env),
            timeout,
            max_output_chars,
            cleanup_dir,
        )
    }

    pub fn start_command_with_environment<F>(
        &self,
        identity: CommandIdentity,
        target: ExecTarget,
        environment_factory: F,
        timeout: Duration,
        max_output_chars: usize,
        cleanup_dir: Option<PathBuf>,
    ) -> Result<CommandHandle, SupervisorError>
    where
        F: FnOnce() -> Result<Vec<(String, String)>, String>,
    {
        self.start_command_with_launch(
            identity,
            target,
            move || {
                let environment = environment_factory()?;
                CommandLaunch::unavailable(environment).map_err(|error| error.to_string())
            },
            timeout,
            max_output_chars,
            cleanup_dir,
        )
    }
}
