use super::{BoundedText, READ_CHUNK_BYTES, TRUNCATION_MARKER};
use regex::Regex;
use std::{
    io::{self, Read},
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
    time::{Duration, Instant},
};

pub const GREP_MAX_SCANNED_BYTES: usize = 32 * 1024 * 1024;
pub const GREP_MAX_ENTRIES: usize = 10_000;
pub const GREP_MAX_ELAPSED: Duration = Duration::from_secs(2);

#[derive(Debug, Clone, Copy)]
pub struct ScanLimits {
    pub max_scanned_bytes: usize,
    pub max_entries: usize,
    pub max_elapsed: Duration,
}

impl ScanLimits {
    pub const fn grep() -> Self {
        Self {
            max_scanned_bytes: GREP_MAX_SCANNED_BYTES,
            max_entries: GREP_MAX_ENTRIES,
            max_elapsed: GREP_MAX_ELAPSED,
        }
    }
}

pub struct ScanBudget {
    limits: ScanLimits,
    started: Instant,
    cancel: Arc<AtomicBool>,
    scanned_bytes: usize,
    entries: usize,
    stopped: bool,
}

impl ScanBudget {
    pub fn new(limits: ScanLimits, cancel: Arc<AtomicBool>) -> Self {
        Self {
            limits,
            started: Instant::now(),
            cancel,
            scanned_bytes: 0,
            entries: 0,
            stopped: false,
        }
    }

    pub fn enter_entry(&mut self) -> bool {
        if !self.check_control() {
            return false;
        }
        self.entries = self.entries.saturating_add(1);
        if self.entries > self.limits.max_entries {
            self.stopped = true;
            return false;
        }
        true
    }

    pub fn read_chunk<R: Read>(
        &mut self,
        reader: &mut R,
        buffer: &mut [u8],
    ) -> io::Result<Option<usize>> {
        if !self.check_control() || self.scanned_bytes >= self.limits.max_scanned_bytes {
            self.stopped = true;
            return Ok(None);
        }
        let remaining = self.limits.max_scanned_bytes - self.scanned_bytes;
        let read_len = buffer.len().min(remaining);
        let read = reader.read(&mut buffer[..read_len])?;
        if read == 0 {
            return Ok(Some(0));
        }
        self.scanned_bytes += read;
        if !self.check_control() {
            return Ok(None);
        }
        Ok(Some(read))
    }

    pub fn stopped(&self) -> bool {
        self.stopped
    }

    pub fn check(&mut self) -> bool {
        self.check_control()
    }

    pub fn scanned_bytes(&self) -> usize {
        self.scanned_bytes
    }

    pub fn entries(&self) -> usize {
        self.entries
    }

    fn check_control(&mut self) -> bool {
        if self.cancel.load(Ordering::Acquire) || self.started.elapsed() >= self.limits.max_elapsed
        {
            self.stopped = true;
            return false;
        }
        true
    }
}

pub fn grep_reader_bounded<R: Read>(
    reader: &mut R,
    display_path: &str,
    pattern: &Regex,
    budget: &mut ScanBudget,
    output: &mut String,
    output_limit: usize,
) -> io::Result<bool> {
    let mut buffer = [0_u8; READ_CHUNK_BYTES];
    let mut line = Vec::new();
    let mut line_number = 1_usize;

    loop {
        let Some(read) = budget.read_chunk(reader, &mut buffer)? else {
            return Ok(true);
        };
        if read == 0 {
            break;
        }
        for byte in &buffer[..read] {
            if *byte == b'\n' {
                if grep_line(
                    display_path,
                    line_number,
                    &line,
                    pattern,
                    output,
                    output_limit,
                ) {
                    return Ok(true);
                }
                line.clear();
                line_number = line_number.saturating_add(1);
            } else {
                line.push(*byte);
            }
        }
    }

    Ok(!line.is_empty()
        && grep_line(
            display_path,
            line_number,
            &line,
            pattern,
            output,
            output_limit,
        ))
}

pub fn finish_bounded_text(text: String, truncated: bool) -> BoundedText {
    if !truncated {
        return BoundedText {
            text,
            truncated: false,
        };
    }
    let mut text = text;
    if !text.ends_with(TRUNCATION_MARKER) {
        text.push_str(TRUNCATION_MARKER);
    }
    BoundedText {
        text,
        truncated: true,
    }
}

fn grep_line(
    display_path: &str,
    line_number: usize,
    bytes: &[u8],
    pattern: &Regex,
    output: &mut String,
    output_limit: usize,
) -> bool {
    let Ok(mut line) = std::str::from_utf8(bytes) else {
        return false;
    };
    if let Some(without_cr) = line.strip_suffix('\r') {
        line = without_cr;
    }
    if !pattern.is_match(line) {
        return false;
    }
    !push_bounded(
        output,
        &format!("{display_path}:{line_number}:"),
        output_limit,
    ) || !push_bounded(output, line, output_limit)
        || !push_bounded(output, "\n", output_limit)
}

fn push_bounded(output: &mut String, value: &str, output_limit: usize) -> bool {
    let remaining = output_limit.saturating_sub(output.len());
    if value.len() <= remaining {
        output.push_str(value);
        return true;
    }
    let mut end = remaining;
    while end > 0 && !value.is_char_boundary(end) {
        end -= 1;
    }
    output.push_str(&value[..end]);
    false
}
