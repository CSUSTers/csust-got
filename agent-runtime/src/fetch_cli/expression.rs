use super::{BodySource, FetchError, FormField, FormPart, JsonField, usage};
use http::{HeaderName, HeaderValue};
use serde_json::Value;
use std::{path::PathBuf, str::FromStr as _};

enum ExpressionSeparator {
    Typed(usize),
    Header(usize),
    String(usize),
    Upload(usize),
}

impl ExpressionSeparator {
    fn classify(expression: &str) -> Option<Self> {
        for (index, byte) in expression.bytes().enumerate() {
            match byte {
                b':' if expression.as_bytes().get(index + 1) == Some(&b'=') => {
                    return Some(Self::Typed(index));
                }
                b':' => return Some(Self::Header(index)),
                b'=' => return Some(Self::String(index)),
                b'@' => return Some(Self::Upload(index)),
                _ => {}
            }
        }
        None
    }
}

pub(super) fn parse_expressions(
    expressions: &[String],
    raw: Option<String>,
    form: bool,
) -> Result<(Vec<(HeaderName, HeaderValue)>, BodySource), FetchError> {
    let mut headers = Vec::new();
    let mut fields = Vec::new();
    let mut files = Vec::new();
    let mut positional_raw = None;
    for expression in expressions {
        if expression.starts_with('@') {
            if positional_raw.replace(expression.clone()).is_some() {
                return Err(usage("multiple raw body sources"));
            }
        } else {
            match ExpressionSeparator::classify(expression) {
                Some(ExpressionSeparator::Typed(index)) => {
                    let name = &expression[..index];
                    let encoded = &expression[index + 2..];
                    validate_field_name(name)?;
                    let value = serde_json::from_str(encoded)
                        .map_err(|_| usage(format!("invalid JSON value for field {name}")))?;
                    fields.push((name.to_string(), value, None));
                }
                Some(ExpressionSeparator::Header(index)) => {
                    headers.push(parse_header(
                        &expression[..index],
                        &expression[index + 1..],
                    )?);
                }
                Some(ExpressionSeparator::String(index)) => {
                    let name = &expression[..index];
                    let value = &expression[index + 1..];
                    validate_field_name(name)?;
                    fields.push((
                        name.to_string(),
                        Value::String(value.to_string()),
                        Some(value.to_string()),
                    ));
                }
                Some(ExpressionSeparator::Upload(index)) => {
                    let name = &expression[..index];
                    let path = &expression[index + 1..];
                    validate_field_name(name)?;
                    if path.is_empty() {
                        return Err(usage("upload path is empty"));
                    }
                    files.push((name.to_string(), PathBuf::from(path)));
                }
                None => {
                    return Err(usage(format!("invalid request expression: {expression}")));
                }
            }
        }
    }
    if raw.is_some() && positional_raw.is_some() {
        return Err(usage("multiple raw body sources"));
    }
    let raw = raw.or(positional_raw);
    if raw.is_some() && (!fields.is_empty() || !files.is_empty() || form) {
        return Err(usage("raw and structured body modes conflict"));
    }
    let body = if let Some(raw) = raw {
        parse_raw(&raw)?
    } else if !files.is_empty() {
        let mut parts = fields
            .into_iter()
            .map(|(name, value, plain)| {
                FormPart::Field(FormField {
                    name,
                    value: plain.unwrap_or_else(|| value.to_string()),
                })
            })
            .collect::<Vec<_>>();
        parts.extend(
            files
                .into_iter()
                .map(|(name, path)| FormPart::File { name, path }),
        );
        BodySource::Multipart(parts)
    } else if form {
        BodySource::Form(
            fields
                .into_iter()
                .map(|(name, value, plain)| FormField {
                    name,
                    value: plain.unwrap_or_else(|| value.to_string()),
                })
                .collect(),
        )
    } else if fields.is_empty() {
        BodySource::Empty
    } else {
        BodySource::Json(
            fields
                .into_iter()
                .map(|(name, value, _)| JsonField { name, value })
                .collect(),
        )
    };
    add_content_type(&mut headers, &body)?;
    Ok((headers, body))
}

fn parse_raw(raw: &str) -> Result<BodySource, FetchError> {
    match raw {
        "@-" => Ok(BodySource::RawStdin),
        value if value.starts_with('@') && value.len() > 1 => {
            Ok(BodySource::RawFile(PathBuf::from(&value[1..])))
        }
        _ => Err(usage("raw body must be @- or @PATH")),
    }
}

fn add_content_type(
    headers: &mut Vec<(HeaderName, HeaderValue)>,
    body: &BodySource,
) -> Result<(), FetchError> {
    let generated = match body {
        BodySource::Empty => return Ok(()),
        BodySource::Json(_) => "application/json",
        BodySource::Form(_) => "application/x-www-form-urlencoded",
        BodySource::Multipart(_) => "multipart/form-data; boundary=agent-runtime-fetch-v1",
        BodySource::RawFile(_) | BodySource::RawStdin => "application/octet-stream",
    };
    if let Some((_, value)) = headers
        .iter()
        .find(|(name, _)| name == http::header::CONTENT_TYPE)
    {
        if matches!(body, BodySource::RawFile(_) | BodySource::RawStdin)
            || value.as_bytes() == generated.as_bytes()
        {
            return Ok(());
        }
        return Err(usage(
            "Content-Type conflicts with the generated structured request body",
        ));
    }
    headers.push((
        http::header::CONTENT_TYPE,
        HeaderValue::from_static(generated),
    ));
    Ok(())
}

fn parse_header(name: &str, value: &str) -> Result<(HeaderName, HeaderValue), FetchError> {
    let name = HeaderName::from_str(name.trim()).map_err(|_| usage("invalid header name"))?;
    if matches!(
        name.as_str(),
        "host"
            | "connection"
            | "proxy-connection"
            | "upgrade"
            | "te"
            | "transfer-encoding"
            | "content-length"
    ) {
        return Err(usage(format!(
            "transport-control header is not supported: {name}"
        )));
    }
    let value = HeaderValue::from_str(value.trim()).map_err(|_| usage("invalid header value"))?;
    Ok((name, value))
}

fn validate_field_name(name: &str) -> Result<(), FetchError> {
    if name.is_empty() || name.contains(['\r', '\n', '"']) {
        Err(usage("invalid field name"))
    } else {
        Ok(())
    }
}
