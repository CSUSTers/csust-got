use agent_runtime::{
    config::FetchClaimLimits,
    fetch_auth::{BrokerAuthCaps, TokenVerifier},
    fetch_protocol::FETCH_PROTOCOL_VERSION,
    runtime_security::{RuntimeFetchSecurity, SHELL_PATH},
    workspace_budget::WorkspaceBudget,
};
use std::{
    collections::BTreeMap,
    fs,
    sync::Arc,
    time::{Duration, SystemTime, UNIX_EPOCH},
};
use tempfile::tempdir;

#[path = "runtime_security/readiness.rs"]
mod readiness;

const KEY: &[u8] = b"a sufficiently long runtime security test key";

fn limits() -> FetchClaimLimits {
    FetchClaimLimits {
        protocol_version: FETCH_PROTOCOL_VERSION,
        policy_version: "policy-v1".to_string(),
        max_concurrency: 2,
        max_requests: 20,
        max_request_bytes: 8 * 1024 * 1024,
        max_response_bytes: 32 * 1024 * 1024,
    }
}

fn verifier() -> TokenVerifier {
    TokenVerifier::new(
        KEY,
        BrokerAuthCaps {
            protocol_version: FETCH_PROTOCOL_VERSION,
            policy_version: "policy-v1".to_string(),
            max_concurrency: 2,
            max_requests: 20,
            max_request_bytes: 8 * 1024 * 1024,
            max_response_bytes: 32 * 1024 * 1024,
            max_future_iat: Duration::from_secs(5),
        },
    )
    .unwrap()
}

#[test]
fn command_tokens_bind_identity_caps_and_effective_timeout() {
    let security =
        RuntimeFetchSecurity::new("/run/agent-fetch/broker.sock", KEY, limits()).unwrap();
    let now = UNIX_EPOCH + Duration::from_secs(2_000_000);

    let first = security
        .issue_command("namespace-a", "run-a", Duration::from_secs(7), now)
        .unwrap();
    let second = security
        .issue_command("namespace-a", "run-a", Duration::from_secs(7), now)
        .unwrap();
    let other_run = security
        .issue_command("namespace-a", "run-b", Duration::from_secs(7), now)
        .unwrap();
    let other_namespace = security
        .issue_command("namespace-b", "run-a", Duration::from_secs(7), now)
        .unwrap();

    assert_eq!(
        first.claims.expires_at_unix - first.claims.issued_at_unix,
        17
    );
    assert_ne!(first.claims.command_id, second.claims.command_id);
    assert_ne!(first.token.expose_secret(), second.token.expose_secret());
    assert_ne!(first.token.expose_secret(), other_run.token.expose_secret());
    assert_ne!(
        first.token.expose_secret(),
        other_namespace.token.expose_secret()
    );
    assert_eq!(first.claims.namespace, "namespace-a");
    assert_eq!(first.claims.run_id, "run-a");
    assert_eq!(first.claims.max_concurrency, 2);
    assert_eq!(first.claims.max_requests, 20);
    assert_eq!(first.claims.max_request_bytes, 8 * 1024 * 1024);
    assert_eq!(first.claims.max_response_bytes, 32 * 1024 * 1024);
    verifier()
        .verify(&first.token, now + Duration::from_secs(1))
        .unwrap();
}

#[test]
fn reduced_request_timeout_controls_token_expiry_not_global_timeout() {
    let security =
        RuntimeFetchSecurity::new("/run/agent-fetch/broker.sock", KEY, limits()).unwrap();
    let now = SystemTime::UNIX_EPOCH + Duration::from_secs(3_000_000);
    let issued = security
        .issue_command("namespace", "run", Duration::from_secs(3), now)
        .unwrap();

    assert_eq!(
        issued.claims.expires_at_unix - issued.claims.issued_at_unix,
        13
    );
    assert_ne!(
        issued.claims.expires_at_unix - issued.claims.issued_at_unix,
        130
    );
}

#[test]
fn shell_environment_contains_only_control_fd_capability() {
    let security =
        RuntimeFetchSecurity::new("/run/agent-fetch/broker.sock", KEY, limits()).unwrap();
    let environment = security.shell_environment();
    let environment = environment.into_iter().collect::<BTreeMap<_, _>>();

    assert_eq!(environment.len(), 3);
    assert_eq!(environment["PATH"], SHELL_PATH);
    assert_eq!(environment["HOME"], "/tmp");
    assert_eq!(environment["AGENT_FETCH_CONTROL_FD"], "4");
    for forbidden in [
        "AGENT_FETCH_SOCKET",
        "AGENT_FETCH_TOKEN",
        "AGENT_FETCH_HMAC_KEY",
        "AGENT_FETCH_HMAC_KEY_FILE",
        "AGENT_RUNTIME_TOKEN",
        "HTTPS_PROXY",
        "BOT_TOKEN",
        "HOST_ONLY_SENTINEL",
    ] {
        assert!(!environment.contains_key(forbidden), "leaked {forbidden}");
    }
}

#[test]
fn logical_workspace_limit_reserves_replacement_delta_before_mutation() {
    let root = tempdir().unwrap();
    let file = root.path().join("note.txt");
    fs::write(&file, b"old-value").unwrap();
    let budget = WorkspaceBudget::new(root.path(), 10).unwrap();

    let mut replacement = budget.begin_replace(&file).unwrap();
    let error = replacement.reserve_total(11).unwrap_err();
    assert_eq!(fs::read(&file).unwrap(), b"old-value");
    assert!(error.to_string().contains("workspace capacity"));
    drop(replacement);

    let mut reservation = budget.begin_replace(&file).unwrap();
    reservation.reserve_total(10).unwrap();
    fs::write(&file, b"new-value!").unwrap();
    reservation.commit();
    assert_eq!(fs::read(&file).unwrap(), b"new-value!");
}

#[test]
fn concurrent_reservations_cannot_overcommit_workspace_limit() {
    let root = tempdir().unwrap();
    let budget = WorkspaceBudget::new(root.path(), 10).unwrap();
    let first = budget
        .reserve_replace(root.path().join("first"), 7)
        .unwrap();

    assert!(
        budget
            .reserve_replace(root.path().join("second"), 4)
            .is_err()
    );
    drop(first);
    assert!(
        budget
            .reserve_replace(root.path().join("second"), 4)
            .is_ok()
    );
}

#[test]
fn shared_old_file_delta_is_reserved_once_under_concurrency() {
    let root = tempdir().unwrap();
    let file = root.path().join("shared");
    fs::write(&file, b"12345678").unwrap();
    let budget = WorkspaceBudget::new(root.path(), 14).unwrap();
    let mut first = budget.begin_replace(&file).unwrap();
    first.reserve_total(12).unwrap();

    assert!(budget.begin_replace(&file).is_err());
    drop(first);
    let mut second = budget.begin_replace(&file).unwrap();
    second.reserve_total(14).unwrap();
}

#[test]
fn incremental_replace_does_not_double_count_its_adjacent_temporary_file() {
    let root = tempdir().unwrap();
    let destination = root.path().join("result");
    let temporary = root.path().join(".result.tmp");
    let budget = WorkspaceBudget::new(root.path(), 10).unwrap();
    let mut reservation = budget.begin_replace(&destination).unwrap();
    reservation.reserve_total(5).unwrap();
    fs::write(&temporary, b"12345").unwrap();

    reservation.reserve_total(10).unwrap();
    fs::write(&temporary, b"1234567890").unwrap();
    fs::rename(&temporary, &destination).unwrap();
    reservation.commit();
    assert_eq!(fs::metadata(destination).unwrap().len(), 10);
}

#[test]
fn underlying_filesystem_capacity_is_checked_before_mutation() {
    let root = tempdir().unwrap();
    let file = root.path().join("note.txt");
    fs::write(&file, b"stable").unwrap();
    let budget =
        WorkspaceBudget::with_capacity_probe(root.path(), 1_024, Arc::new(|_| Ok(0))).unwrap();

    let error = budget.reserve_replace(&file, 7).unwrap_err();
    assert_eq!(fs::read(&file).unwrap(), b"stable");
    assert!(error.to_string().contains("filesystem capacity"));
}

#[test]
fn token_and_security_debug_output_never_contains_secrets() {
    let security =
        RuntimeFetchSecurity::new("/run/agent-fetch/broker.sock", KEY, limits()).unwrap();
    let issued = security
        .issue_command(
            "namespace",
            "run",
            Duration::from_secs(5),
            UNIX_EPOCH + Duration::from_secs(5_000_000),
        )
        .unwrap();

    assert!(!format!("{security:?}").contains(std::str::from_utf8(KEY).unwrap()));
    assert!(!format!("{issued:?}").contains(issued.token.expose_secret()));
}
