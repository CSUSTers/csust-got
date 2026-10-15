use super::{ResolveError, Resolver};
use async_trait::async_trait;
use hickory_resolver::{
    TokioResolver,
    config::{LookupIpStrategy, NameServerConfig, ResolveHosts, ResolverConfig},
    net::{NetError, runtime::TokioRuntimeProvider},
};
use std::{
    fmt,
    net::{IpAddr, SocketAddr},
    time::Duration,
};
use tokio::time::timeout;

#[derive(Clone)]
pub struct DedicatedResolver {
    resolver: TokioResolver,
    timeout: Duration,
}

impl DedicatedResolver {
    pub fn new(servers: &[SocketAddr], timeout: Duration) -> Result<Self, NetError> {
        let mut config = ResolverConfig::default();
        for socket_addr in servers {
            let mut name_server = NameServerConfig::udp_and_tcp(socket_addr.ip());
            for connection in &mut name_server.connections {
                connection.port = socket_addr.port();
            }
            config.add_name_server(name_server);
        }
        let mut builder = TokioResolver::builder_with_config(config, TokioRuntimeProvider::new());
        let options = builder.options_mut();
        options.use_hosts_file = ResolveHosts::Never;
        options.ip_strategy = LookupIpStrategy::Ipv4AndIpv6;
        options.timeout = timeout;
        options.attempts = 1;
        options.cache_size = 0;
        Ok(Self {
            resolver: builder.build()?,
            timeout,
        })
    }
}

impl fmt::Debug for DedicatedResolver {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str("DedicatedResolver")
    }
}

#[async_trait]
impl Resolver for DedicatedResolver {
    async fn resolve_all(&self, host: &str) -> Result<Vec<IpAddr>, ResolveError> {
        if let Ok(address) = host.parse() {
            return Ok(vec![address]);
        }
        let lookup = timeout(self.timeout, self.resolver.lookup_ip(host))
            .await
            .map_err(|_| ResolveError::Timeout)?
            .map_err(|_| ResolveError::Failed)?;
        let mut answers = lookup.iter().collect::<Vec<_>>();
        answers.sort_unstable();
        answers.dedup();
        Ok(answers)
    }
}
