use super::{BodySource, FetchCli, FetchError, expression::parse_expressions, usage};
use http::Method;
use std::{path::PathBuf, time::Duration};

pub(super) fn parse<I, S>(argv: I) -> Result<FetchCli, FetchError>
where
    I: IntoIterator<Item = S>,
    S: AsRef<str>,
{
    let mut args = argv.into_iter().map(|value| value.as_ref().to_string());
    let _program = args.next();
    let mut positionals = Vec::new();
    let mut raw = None;
    let mut output = None;
    let mut request_timeout = None;
    let mut follow = false;
    let mut show_headers = false;
    let mut check_status = false;
    let mut form = false;
    let mut args = args.peekable();
    while let Some(arg) = args.next() {
        match arg.as_str() {
            "--follow" => follow = true,
            "--headers" => show_headers = true,
            "--check-status" => check_status = true,
            "--form" => form = true,
            "--output" => set_once(
                &mut output,
                PathBuf::from(required_option(&mut args, "--output")?),
                "--output",
            )?,
            "--timeout" => set_once(
                &mut request_timeout,
                parse_timeout(&required_option(&mut args, "--timeout")?)?,
                "--timeout",
            )?,
            "--raw" => set_once(&mut raw, required_option(&mut args, "--raw")?, "--raw")?,
            value if value.starts_with("--timeout=") => set_once(
                &mut request_timeout,
                parse_timeout(&value[10..])?,
                "--timeout",
            )?,
            value if value.starts_with("--output=") => {
                set_once(&mut output, PathBuf::from(&value[9..]), "--output")?
            }
            value if value.starts_with("--raw=") => {
                set_once(&mut raw, value[6..].to_string(), "--raw")?
            }
            value if value.starts_with('-') => {
                return Err(usage(format!("unsupported option: {value}")));
            }
            value => positionals.push(value.to_string()),
        }
    }
    if positionals.is_empty() {
        return Err(usage("missing URL"));
    }

    let (explicit_method, url_index) =
        if positionals.len() >= 2 && valid_method_token(&positionals[0]) {
            (Some(parse_method(&positionals[0])?), 1)
        } else {
            (None, 0)
        };
    let url = positionals[url_index].clone();
    if url.is_empty()
        || url.chars().any(char::is_whitespace)
        || url.chars().any(char::is_control)
        || looks_like_expression(&url)
    {
        return Err(usage("invalid URL argument"));
    }
    let (headers, body) = parse_expressions(&positionals[url_index + 1..], raw, form)?;
    let method = match explicit_method {
        Some(method) => method,
        None if matches!(body, BodySource::Empty) => Method::GET,
        None => Method::POST,
    };
    if method == Method::CONNECT {
        return Err(usage("CONNECT is not supported"));
    }
    if output
        .as_ref()
        .is_some_and(|path| path.as_os_str().is_empty())
    {
        return Err(usage("--output requires a non-empty path"));
    }
    Ok(FetchCli {
        method,
        url,
        headers,
        body,
        follow,
        show_headers,
        check_status,
        output,
        timeout: request_timeout,
    })
}

fn parse_method(raw: &str) -> Result<Method, FetchError> {
    Method::from_bytes(raw.as_bytes()).map_err(|_| usage("invalid HTTP method token"))
}

fn valid_method_token(value: &str) -> bool {
    !value.is_empty()
        && value
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || b"!#$%&'*+-.^_`|~".contains(&byte))
}

fn looks_like_expression(value: &str) -> bool {
    value.starts_with('@')
        || value.contains(":=")
        || (!value.contains("://") && (value.contains('=') || value.contains('@')))
}

fn parse_timeout(raw: &str) -> Result<Duration, FetchError> {
    let duration = if let Some(value) = raw.strip_suffix("ms") {
        Duration::from_millis(value.parse().map_err(|_| usage("invalid timeout"))?)
    } else {
        Duration::from_secs(
            raw.strip_suffix('s')
                .unwrap_or(raw)
                .parse()
                .map_err(|_| usage("invalid timeout"))?,
        )
    };
    if duration.is_zero() || duration > Duration::from_secs(30) {
        Err(usage("timeout must be greater than zero and at most 30s"))
    } else {
        Ok(duration)
    }
}

fn required_option<I: Iterator<Item = String>>(
    args: &mut I,
    option: &str,
) -> Result<String, FetchError> {
    args.next()
        .filter(|value| !value.is_empty())
        .ok_or_else(|| usage(format!("{option} requires a value")))
}

fn set_once<T>(slot: &mut Option<T>, value: T, option: &str) -> Result<(), FetchError> {
    if slot.replace(value).is_some() {
        Err(usage(format!("{option} may only be specified once")))
    } else {
        Ok(())
    }
}
