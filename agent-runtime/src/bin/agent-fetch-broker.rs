#[cfg(target_os = "linux")]
#[tokio::main]
async fn main() {
    use agent_runtime::fetch_broker::{BrokerConfig, FetchBroker};

    if handle_arguments() {
        return;
    }

    let config = match BrokerConfig::from_env(|name| std::env::var(name).ok()) {
        Ok(config) => config,
        Err(error) => {
            eprintln!("agent-fetch-broker: {error}");
            std::process::exit(69);
        }
    };
    let broker = match FetchBroker::from_config(config).await {
        Ok(broker) => broker,
        Err(error) => {
            eprintln!("agent-fetch-broker: {error}");
            std::process::exit(69);
        }
    };
    if let Err(error) = broker.serve().await {
        eprintln!("agent-fetch-broker: {error}");
        std::process::exit(69);
    }
}

#[cfg(not(target_os = "linux"))]
fn main() {
    if handle_arguments() {
        return;
    }
    eprintln!("fetch broker production execution requires Linux");
    std::process::exit(69);
}

fn handle_arguments() -> bool {
    let arguments = std::env::args().skip(1).collect::<Vec<_>>();
    if arguments.is_empty() {
        return false;
    }
    if arguments.len() == 1 && matches!(arguments[0].as_str(), "--help" | "-h") {
        println!(
            "Usage: agent-fetch-broker\n\nConfiguration is read from AGENT_FETCH_* environment variables."
        );
        return true;
    }
    eprintln!("agent-fetch-broker: unexpected command-line arguments");
    std::process::exit(64);
}
