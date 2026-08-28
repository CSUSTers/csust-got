use super::super::*;

pub(in crate::exec) struct CollectedOutput {
    pub(super) text: String,
    pub(super) truncated: bool,
}

pub(super) async fn collect_output<R>(mut reader: R, limit: usize) -> io::Result<CollectedOutput>
where
    R: AsyncRead + Unpin,
{
    let mut bytes = Vec::with_capacity(limit.min(OUTPUT_READ_BUFFER_SIZE));
    let mut buffer = [0_u8; OUTPUT_READ_BUFFER_SIZE];
    let mut truncated = false;
    loop {
        let read = reader.read(&mut buffer).await?;
        if read == 0 {
            break;
        }
        if limit == 0 {
            bytes.extend_from_slice(&buffer[..read]);
            continue;
        }
        let retained = read.min(limit.saturating_sub(bytes.len()));
        bytes.extend_from_slice(&buffer[..retained]);
        truncated |= retained < read;
    }
    let mut text = String::from_utf8_lossy(&bytes).into_owned();
    if truncated {
        if text.len() > bytes.len() {
            let mut end = bytes.len();
            while !text.is_char_boundary(end) {
                end -= 1;
            }
            text.truncate(end);
        }
        text.push_str(TRUNCATION_MARKER);
    }
    Ok(CollectedOutput { text, truncated })
}

pub(super) async fn join_collector(
    task: JoinHandle<io::Result<CollectedOutput>>,
    stream: &str,
) -> Result<CollectedOutput, SupervisorError> {
    task.await
        .map_err(|error| SupervisorError::Command(format!("{stream} collector failed: {error}")))?
        .map_err(|error| SupervisorError::Command(format!("{stream} collection failed: {error}")))
}
