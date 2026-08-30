use super::{ResolveError, Resolver};
use async_trait::async_trait;
use hickory_resolver::{
    TokioResolver,
    config::{
        LookupIpStrategy, NameServerConfig, NameServerConfigGroup, ResolveHosts, ResolverConfig,
    },
    name_server::TokioConnectionProvider,
    proto::xfer::Protocol,
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
    pub fn new(servers: &[SocketAddr], timeout: Duration) -> Self {
        let mut name_servers = NameServerConfigGroup::new();
        for socket_addr in servers {
            for protocol in [Protocol::Udp, Protocol::Tcp] {
                name_servers.push(NameServerConfig::new(*socket_addr, protocol));
            }
        }
        let mut builder = TokioResolver::builder_with_config(
            ResolverConfig::from_parts(None, Vec::new(), name_servers),
            TokioConnectionProvider::default(),
        );
        let options = builder.options_mut();
        options.use_hosts_file = ResolveHosts::Never;
        options.ip_strategy = LookupIpStrategy::Ipv4AndIpv6;
        options.timeout = timeout;
        options.attempts = 1;
        options.cache_size = 0;
        Self {
            resolver: builder.build(),
            timeout,
        }
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
