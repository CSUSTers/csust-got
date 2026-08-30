#[cfg(unix)]
#[tokio::main]
async fn main() {
    use agent_runtime::fetch_cli::{FetchCli, FetchExit, run_fetch};

    let cli = match FetchCli::parse(std::env::args()) {
        Ok(cli) => cli,
        Err(error) => {
            eprintln!("fetch: {error}");
            std::process::exit(error.exit_code() as i32);
        }
    };
    match run_fetch(
        cli,
        tokio::io::stdin(),
        tokio::io::stdout(),
        tokio::io::stderr(),
    )
    .await
    {
        Ok(()) => std::process::exit(FetchExit::Success as i32),
        Err(error) if error.is_broken_pipe() => {
            std::process::exit(FetchExit::Success as i32);
        }
        Err(error) => {
            eprintln!("fetch: {error}");
            std::process::exit(error.exit_code() as i32);
        }
    }
}

#[cfg(not(unix))]
fn main() {
    eprintln!("fetch: Unix-domain sockets are unavailable on this platform");
    std::process::exit(agent_runtime::fetch_cli::FetchExit::Unavailable as i32);
}
