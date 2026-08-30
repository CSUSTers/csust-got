use super::{CommandLifecycleLease, RuntimeFetchProxy, RuntimeFetchProxyError};
#[cfg(target_os = "linux")]
use super::{
    ProxyMode,
    lifecycle::CommandBindingPhase,
    registry::{BindingContext, BindingEntry},
};
use crate::exec::BashHealth;
#[cfg(target_os = "linux")]
use crate::identity::namespace_storage_key;
#[cfg(target_os = "linux")]
use std::sync::{Arc, Mutex};
use std::time::Duration;
#[cfg(target_os = "linux")]
use tokio::sync::Mutex as AsyncMutex;
#[cfg(target_os = "linux")]
use tokio_util::sync::CancellationToken;

pub struct CommandBindingLease {
    lifecycle: CommandLifecycleLease,
    #[cfg(target_os = "linux")]
    control_source: Option<std::os::fd::OwnedFd>,
}

pub struct CommandLaunch {
    pub environment: Vec<(String, String)>,
    pub lifecycle: CommandLifecycleLease,
    #[cfg(target_os = "linux")]
    pub control_source: std::os::fd::OwnedFd,
}

impl RuntimeFetchProxy {
    pub fn active_binding_count(&self) -> Result<usize, RuntimeFetchProxyError> {
        self.inner
            .registry
            .lock()
            .map(|registry| registry.len())
            .map_err(|_| RuntimeFetchProxyError::new("fetch proxy registry is poisoned"))
    }

    pub fn bind_command(
        &self,
        namespace: &str,
        run_id: &str,
        command_id: String,
        effective_timeout: Duration,
        health: BashHealth,
    ) -> Result<CommandBindingLease, RuntimeFetchProxyError> {
        #[cfg(not(target_os = "linux"))]
        {
            let _ = (namespace, run_id, command_id, effective_timeout, health);
            return Ok(CommandBindingLease {
                lifecycle: CommandLifecycleLease::from_entry(None),
            });
        }
        #[cfg(target_os = "linux")]
        {
            use std::time::SystemTime;

            let (runtime_endpoint, command_endpoint) =
                super::socket::command_control_socket_pair()?;
            let (issued, broker_socket) = match &self.inner.mode {
                ProxyMode::Disabled => (None, None),
                ProxyMode::Enabled(security) => {
                    let issued = security
                        .issue_for_command(
                            namespace,
                            run_id,
                            command_id.clone(),
                            effective_timeout,
                            SystemTime::now(),
                        )
                        .map_err(|error| RuntimeFetchProxyError::new(error.to_string()))?;
                    (Some(issued), Some(security.socket_path().to_path_buf()))
                }
            };
            let context = Arc::new(BindingContext {
                phase: Arc::new(Mutex::new(CommandBindingPhase::Active)),
                namespace: namespace.to_string(),
                namespace_key: namespace_storage_key(namespace),
                workspace_budget: self.inner.workspace_budget.clone(),
                health,
                issued,
                broker_socket,
                session_permits: Arc::new(tokio::sync::Semaphore::new(
                    super::MAX_ACTIVE_LOCAL_SESSIONS,
                )),
            });
            let (job_sender, job_receiver) =
                tokio::sync::mpsc::channel(super::SESSION_JOB_CHANNEL_CAPACITY);
            let control_cancel = CancellationToken::new();
            let guardian_cancel = CancellationToken::new();
            let entry = Arc::new(BindingEntry {
                command_id: command_id.clone(),
                context,
                control_cancel,
                guardian_cancel,
                control_reader: AsyncMutex::new(None),
                control_outcome: AsyncMutex::new(None),
                guardian: AsyncMutex::new(None),
                guardian_outcome: AsyncMutex::new(None),
                job_sender: Mutex::new(Some(job_sender.clone())),
                proxy: Arc::downgrade(&self.inner),
            });
            let mut registry = self
                .inner
                .registry
                .lock()
                .map_err(|_| RuntimeFetchProxyError::new("fetch proxy registry is poisoned"))?;
            if registry.contains_key(&command_id) {
                return Err(RuntimeFetchProxyError::new(
                    "command binding identity is already registered",
                ));
            }
            let guardian_entry = Arc::clone(&entry);
            let guardian = tokio::spawn(async move {
                super::guardian::session_guardian(
                    job_receiver,
                    Arc::clone(&guardian_entry.context),
                    guardian_entry.guardian_cancel.clone(),
                )
                .await
            });
            let control_entry = Arc::clone(&entry);
            let control_reader = tokio::spawn(async move {
                super::control::control_reader(
                    runtime_endpoint,
                    Arc::clone(&control_entry.context.phase),
                    Arc::clone(&control_entry.context.session_permits),
                    control_entry.control_cancel.clone(),
                    job_sender,
                )
                .await
            });
            *entry.guardian.try_lock().map_err(|_| {
                RuntimeFetchProxyError::new("fetch proxy guardian slot unexpectedly busy")
            })? = Some(guardian);
            *entry.control_reader.try_lock().map_err(|_| {
                RuntimeFetchProxyError::new("fetch proxy control slot unexpectedly busy")
            })? = Some(control_reader);
            registry.insert(command_id, Arc::clone(&entry));
            drop(registry);
            Ok(CommandBindingLease {
                lifecycle: CommandLifecycleLease::from_entry(Some(entry)),
                control_source: Some(command_endpoint),
            })
        }
    }
}

impl CommandBindingLease {
    pub fn lifecycle(&self) -> CommandLifecycleLease {
        self.lifecycle.clone()
    }

    pub async fn revoke_and_wait(
        &self,
    ) -> Result<super::BindingDrainReceipt, super::BindingDrainError> {
        self.lifecycle.revoke_and_wait().await
    }

    #[allow(unused_mut)]
    pub fn into_launch(
        mut self,
        environment: Vec<(String, String)>,
    ) -> Result<CommandLaunch, RuntimeFetchProxyError> {
        #[cfg(target_os = "linux")]
        {
            let control_source = self.control_source.take().ok_or_else(|| {
                RuntimeFetchProxyError::new("command-control descriptor is unavailable")
            })?;
            Ok(CommandLaunch {
                environment,
                lifecycle: self.lifecycle.clone(),
                control_source,
            })
        }
        #[cfg(not(target_os = "linux"))]
        {
            Ok(CommandLaunch {
                environment,
                lifecycle: self.lifecycle.clone(),
            })
        }
    }
}

impl CommandLaunch {
    #[cfg(any(test, feature = "c7-test-support"))]
    pub(crate) fn with_lifecycle_for_tests(
        environment: Vec<(String, String)>,
        lifecycle: CommandLifecycleLease,
    ) -> Result<Self, RuntimeFetchProxyError> {
        #[cfg(target_os = "linux")]
        {
            let (runtime, control_source) = super::socket::command_control_socket_pair()?;
            drop(runtime);
            Ok(Self {
                environment,
                lifecycle,
                control_source,
            })
        }
        #[cfg(not(target_os = "linux"))]
        {
            Ok(Self {
                environment,
                lifecycle,
            })
        }
    }

    pub fn unavailable(environment: Vec<(String, String)>) -> Result<Self, RuntimeFetchProxyError> {
        #[cfg(target_os = "linux")]
        {
            let (runtime, control_source) = super::socket::command_control_socket_pair()?;
            drop(runtime);
            Ok(Self {
                environment,
                lifecycle: CommandLifecycleLease::detached_for_tests(),
                control_source,
            })
        }
        #[cfg(not(target_os = "linux"))]
        {
            Ok(Self {
                environment,
                lifecycle: CommandLifecycleLease::detached_for_tests(),
            })
        }
    }
}
