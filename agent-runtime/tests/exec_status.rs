use agent_runtime::exec::{
    EXEC_STARTUP_TIMEOUT, EXEC_STATUS_RECORD_BYTES, ExecInitStage, ExecStartupChannelError,
    ExecStartupOutcome, ExecStatusRecord, await_exec_status, encode_exec_status_failure,
};
use std::time::Duration;
use tokio::io::{AsyncWriteExt as _, duplex};

#[tokio::test]
async fn helper_status_accepts_only_clean_eof_or_one_exact_failure_record() {
    let (writer, mut reader) = duplex(64);
    drop(writer);
    assert_eq!(
        await_exec_status(&mut reader, Duration::from_millis(50))
            .await
            .unwrap(),
        ExecStartupOutcome::TargetExecSucceeded
    );

    assert_eq!(EXEC_STATUS_RECORD_BYTES, 4);
    assert_eq!(EXEC_STARTUP_TIMEOUT, Duration::from_secs(2));
    for stage in ExecInitStage::ALL {
        let (mut writer, mut reader) = duplex(64);
        writer
            .write_all(&encode_exec_status_failure(stage))
            .await
            .unwrap();
        drop(writer);
        assert_eq!(
            await_exec_status(&mut reader, Duration::from_millis(50))
                .await
                .unwrap(),
            ExecStartupOutcome::HelperFailed(ExecStatusRecord { stage })
        );
    }
}

#[tokio::test]
async fn helper_status_rejects_malformed_truncated_multiple_and_timeout() {
    for payload in [
        vec![0xa7],
        encode_exec_status_failure(ExecInitStage::ConfigRead)[..3].to_vec(),
        [
            encode_exec_status_failure(ExecInitStage::ConfigRead).as_slice(),
            &[0],
        ]
        .concat(),
    ] {
        let (mut writer, mut reader) = duplex(64);
        writer.write_all(&payload).await.unwrap();
        drop(writer);
        assert!(
            await_exec_status(&mut reader, Duration::from_millis(50))
                .await
                .is_err()
        );
    }

    let (_writer, mut reader) = duplex(64);
    let error = await_exec_status(&mut reader, Duration::from_millis(10))
        .await
        .unwrap_err();
    assert!(matches!(error, ExecStartupChannelError::Timeout));
}
