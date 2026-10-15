use agent_runtime::fetch_broker::{DedicatedResolver, ResolveError, Resolver};
use hickory_resolver::proto::{
    op::{Message, MessageType, ResponseCode},
    rr::{
        RData, Record, RecordType,
        rdata::{A, AAAA},
    },
};
use std::{
    net::{IpAddr, Ipv4Addr, Ipv6Addr, SocketAddr},
    sync::{Arc, Mutex},
    time::Duration,
};
use tokio::{
    io::{AsyncReadExt as _, AsyncWriteExt as _},
    net::{TcpListener, TcpStream, UdpSocket},
    task::JoinSet,
    time::timeout,
};

const DNS_TIMEOUT: Duration = Duration::from_secs(2);
const TEST_TIMEOUT: Duration = Duration::from_secs(5);

#[derive(Clone, Copy)]
enum Reply {
    Answers,
    Truncated,
    NxDomain,
    Silent,
}

struct DnsFixture {
    address: SocketAddr,
    queries: Arc<Mutex<Vec<(bool, RecordType)>>>,
    tasks: JoinSet<()>,
}

impl DnsFixture {
    async fn start(reply: Reply) -> Self {
        let tcp = TcpListener::bind((Ipv4Addr::LOCALHOST, 0)).await.unwrap();
        let address = tcp.local_addr().unwrap();
        let udp = UdpSocket::bind(address).await.unwrap();
        let queries = Arc::new(Mutex::new(Vec::new()));
        let mut tasks = JoinSet::new();
        let udp_queries = Arc::clone(&queries);
        tasks.spawn(async move {
            let mut buffer = [0; 4096];
            loop {
                let (length, peer) = udp.recv_from(&mut buffer).await.unwrap();
                if let Some(response) = dns_response(&buffer[..length], false, reply, &udp_queries)
                {
                    udp.send_to(&response, peer).await.unwrap();
                }
            }
        });
        let tcp_queries = Arc::clone(&queries);
        tasks.spawn(async move {
            let mut clients = JoinSet::new();
            loop {
                tokio::select! {
                    accepted = tcp.accept() => {
                        let (stream, _) = accepted.unwrap();
                        clients.spawn(serve_tcp(stream, reply, Arc::clone(&tcp_queries)));
                    }
                    result = clients.join_next(), if !clients.is_empty() => {
                        result.unwrap().unwrap();
                    }
                }
            }
        });
        Self {
            address,
            queries,
            tasks,
        }
    }

    fn resolver(&self, timeout: Duration) -> DedicatedResolver {
        DedicatedResolver::new(&[self.address], timeout).unwrap()
    }

    fn assert_queries(&self, tcp: bool) {
        let queries = self.queries.lock().unwrap();
        assert!(queries.contains(&(tcp, RecordType::A)));
        assert!(queries.contains(&(tcp, RecordType::AAAA)));
    }

    async fn stop(mut self) {
        self.tasks.abort_all();
        while let Some(result) = self.tasks.join_next().await {
            if let Err(error) = result {
                assert!(error.is_cancelled(), "DNS fixture failed: {error}");
            }
        }
    }
}

async fn serve_tcp(
    mut stream: TcpStream,
    reply: Reply,
    queries: Arc<Mutex<Vec<(bool, RecordType)>>>,
) {
    while let Ok(length) = stream.read_u16().await {
        let mut buffer = vec![0; usize::from(length)];
        stream.read_exact(&mut buffer).await.unwrap();
        if let Some(response) = dns_response(&buffer, true, reply, &queries) {
            stream
                .write_u16(response.len().try_into().unwrap())
                .await
                .unwrap();
            stream.write_all(&response).await.unwrap();
        }
    }
}

fn dns_response(
    request: &[u8],
    tcp: bool,
    reply: Reply,
    queries: &Mutex<Vec<(bool, RecordType)>>,
) -> Option<Vec<u8>> {
    let mut response = Message::from_vec(request).unwrap();
    let query = response.queries[0].clone();
    queries.lock().unwrap().push((tcp, query.query_type()));
    if matches!(reply, Reply::Silent) {
        return None;
    }
    response.metadata.message_type = MessageType::Response;
    response.metadata.authoritative = true;
    response.metadata.recursion_available = true;
    if matches!(reply, Reply::NxDomain) {
        response.metadata.response_code = ResponseCode::NXDomain;
    } else if matches!(reply, Reply::Truncated) && !tcp {
        response.metadata.truncation = true;
    } else {
        let answers = match query.query_type() {
            RecordType::A => vec![
                RData::A(A(Ipv4Addr::new(192, 0, 2, 9))),
                RData::A(A(Ipv4Addr::new(192, 0, 2, 1))),
                RData::A(A(Ipv4Addr::new(192, 0, 2, 9))),
            ],
            RecordType::AAAA => vec![
                RData::AAAA(AAAA("2001:db8::9".parse().unwrap())),
                RData::AAAA(AAAA("2001:db8::1".parse().unwrap())),
                RData::AAAA(AAAA("2001:db8::9".parse().unwrap())),
            ],
            other => panic!("unexpected DNS query: {other:?}"),
        };
        for answer in answers {
            response.add_answer(Record::from_rdata(query.name().clone(), 60, answer));
        }
    }
    Some(response.to_vec().unwrap())
}

fn expected_addresses() -> Vec<IpAddr> {
    ["192.0.2.1", "192.0.2.9", "2001:db8::1", "2001:db8::9"]
        .map(|address| address.parse().unwrap())
        .to_vec()
}

#[tokio::test]
async fn custom_port_resolves_both_families_sorted_and_deduplicated() {
    let fixture = DnsFixture::start(Reply::Answers).await;
    let resolver = fixture.resolver(DNS_TIMEOUT);
    let answers = timeout(TEST_TIMEOUT, resolver.resolve_all("addresses.test."))
        .await
        .unwrap()
        .unwrap();
    assert_eq!(answers, expected_addresses());
    fixture.assert_queries(false);
    assert!(fixture.queries.lock().unwrap().iter().all(|(tcp, _)| !tcp));
    fixture.stop().await;
}

#[tokio::test]
async fn truncated_udp_retries_over_tcp_on_the_custom_port() {
    let fixture = DnsFixture::start(Reply::Truncated).await;
    let resolver = fixture.resolver(DNS_TIMEOUT);
    let answers = timeout(TEST_TIMEOUT, resolver.resolve_all("truncated.test."))
        .await
        .unwrap()
        .unwrap();
    assert_eq!(answers, expected_addresses());
    fixture.assert_queries(false);
    fixture.assert_queries(true);
    fixture.stop().await;
}

#[tokio::test]
async fn literal_addresses_bypass_dns() {
    let fixture = DnsFixture::start(Reply::Answers).await;
    let resolver = fixture.resolver(DNS_TIMEOUT);
    for address in [
        IpAddr::V4(Ipv4Addr::LOCALHOST),
        IpAddr::V6(Ipv6Addr::LOCALHOST),
    ] {
        assert_eq!(
            timeout(TEST_TIMEOUT, resolver.resolve_all(&address.to_string()))
                .await
                .unwrap()
                .unwrap(),
            vec![address],
        );
    }
    assert!(fixture.queries.lock().unwrap().is_empty());
    fixture.stop().await;
}

#[tokio::test]
async fn dns_failure_is_reported_without_fallback() {
    let fixture = DnsFixture::start(Reply::NxDomain).await;
    let resolver = fixture.resolver(DNS_TIMEOUT);
    assert!(matches!(
        timeout(TEST_TIMEOUT, resolver.resolve_all("missing.test."))
            .await
            .unwrap(),
        Err(ResolveError::Failed),
    ));
    fixture.assert_queries(false);
    fixture.stop().await;
}

#[tokio::test]
async fn unresponsive_dns_fails_within_the_deadline() {
    let fixture = DnsFixture::start(Reply::Silent).await;
    let resolver = fixture.resolver(Duration::from_millis(100));
    assert!(matches!(
        timeout(TEST_TIMEOUT, resolver.resolve_all("silent.test."))
            .await
            .unwrap(),
        Err(ResolveError::Timeout | ResolveError::Failed),
    ));
    fixture.assert_queries(false);
    fixture.stop().await;
}
