use super::*;
use regex::Regex;
use std::{
    io::{self, Cursor, Read},
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
    time::Duration,
};

#[test]
fn bounded_read_returns_partial_utf8_text_with_one_marker() {
    let mut reader = Cursor::new("你好hello".as_bytes());

    let text = read_text_bounded(&mut reader, 4).unwrap();

    assert!(text.truncated);
    assert_eq!(text.text, format!("你{TRUNCATION_MARKER}"));
    assert_eq!(text.text.matches(TRUNCATION_MARKER).count(), 1);
}

#[test]
fn bounded_read_does_not_consume_the_full_large_source() {
    struct CountingReader {
        bytes: usize,
        reads: Arc<std::sync::atomic::AtomicUsize>,
    }

    impl Read for CountingReader {
        fn read(&mut self, buffer: &mut [u8]) -> io::Result<usize> {
            if self.bytes == 0 {
                return Ok(0);
            }
            let read = self.bytes.min(buffer.len());
            buffer[..read].fill(b'x');
            self.bytes -= read;
            self.reads.fetch_add(read, Ordering::SeqCst);
            Ok(read)
        }
    }

    let reads = Arc::new(std::sync::atomic::AtomicUsize::new(0));
    let mut reader = CountingReader {
        bytes: 1024 * 1024,
        reads: Arc::clone(&reads),
    };

    let text = read_text_bounded(&mut reader, 16).unwrap();

    assert!(text.truncated);
    assert!(reads.load(Ordering::SeqCst) <= 20);
    assert!(reads.load(Ordering::SeqCst) < 1024 * 1024);
}

#[test]
fn grep_stops_at_byte_and_elapsed_budgets_with_one_marker() {
    let pattern = Regex::new("needle").unwrap();
    let cancel = Arc::new(AtomicBool::new(false));
    let limits = ScanLimits {
        max_scanned_bytes: 8,
        max_entries: 10,
        max_elapsed: Duration::from_secs(1),
    };
    let mut budget = ScanBudget::new(limits, Arc::clone(&cancel));
    let mut output = String::new();
    let mut reader = Cursor::new(b"needle\nneedle\n");

    let stopped = grep_reader_bounded(
        &mut reader,
        "/skills/a",
        &pattern,
        &mut budget,
        &mut output,
        128,
    )
    .unwrap();
    let output = finish_bounded_text(output, stopped || budget.stopped());

    assert!(output.truncated);
    assert_eq!(budget.scanned_bytes(), 8);
    assert_eq!(output.text.matches(TRUNCATION_MARKER).count(), 1);

    let elapsed_limits = ScanLimits {
        max_scanned_bytes: 128,
        max_entries: 10,
        max_elapsed: Duration::ZERO,
    };
    let mut elapsed_budget = ScanBudget::new(elapsed_limits, cancel);
    let mut elapsed_output = String::new();
    let mut elapsed_reader = Cursor::new(b"needle\n");
    let stopped = grep_reader_bounded(
        &mut elapsed_reader,
        "/skills/a",
        &pattern,
        &mut elapsed_budget,
        &mut elapsed_output,
        128,
    )
    .unwrap();
    let elapsed_output = finish_bounded_text(
        stopped.then_some(String::new()).unwrap_or(elapsed_output),
        stopped || elapsed_budget.stopped(),
    );

    assert!(elapsed_output.truncated);
    assert_eq!(elapsed_output.text.matches(TRUNCATION_MARKER).count(), 1);
}

#[test]
fn grep_entry_budget_counts_skipped_entries() {
    let mut budget = ScanBudget::new(
        ScanLimits {
            max_scanned_bytes: 128,
            max_entries: 1,
            max_elapsed: Duration::from_secs(1),
        },
        Arc::new(AtomicBool::new(false)),
    );

    assert!(budget.enter_entry());
    assert!(!budget.enter_entry());
    assert_eq!(budget.entries(), 2);
    assert!(budget.stopped());
}

#[test]
fn grep_output_limit_returns_one_marker_without_retaining_extra_text() {
    let mut budget = ScanBudget::new(
        ScanLimits {
            max_scanned_bytes: 128,
            max_entries: 10,
            max_elapsed: Duration::from_secs(1),
        },
        Arc::new(AtomicBool::new(false)),
    );
    let pattern = Regex::new("needle").unwrap();
    let mut reader = Cursor::new(b"needle\n");
    let mut output = String::new();

    let truncated = grep_reader_bounded(
        &mut reader,
        "/skills/a",
        &pattern,
        &mut budget,
        &mut output,
        8,
    )
    .unwrap();
    let output = finish_bounded_text(output, truncated || budget.stopped());

    assert!(output.truncated);
    assert!(output.text.len() <= 8 + TRUNCATION_MARKER.len());
    assert_eq!(output.text.matches(TRUNCATION_MARKER).count(), 1);
}

#[tokio::test]
async fn dropping_cancellable_worker_notifies_the_blocking_closure() {
    let (started_tx, started_rx) = std::sync::mpsc::sync_channel(1);
    let (stopped_tx, stopped_rx) = tokio::sync::oneshot::channel();
    let worker = spawn_cancellable_blocking(move |cancel| {
        started_tx.send(()).unwrap();
        while !cancel.load(Ordering::Acquire) {
            std::thread::yield_now();
        }
        stopped_tx.send(()).unwrap();
    });

    tokio::task::spawn_blocking(move || started_rx.recv().unwrap())
        .await
        .unwrap();
    drop(worker);
    stopped_rx.await.unwrap();
}
