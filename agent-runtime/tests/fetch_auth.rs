use agent_runtime::fetch_auth::{
    AuthErrorKind, BrokerAuthCaps, CommandIdentity, FetchClaims, QuotaErrorKind, QuotaRegistry,
    TokenIssuer, TokenVerifier,
};
use agent_runtime::fetch_protocol::{FETCH_PROTOCOL_VERSION, SecretString};
use std::time::{Duration, UNIX_EPOCH};

const SIGNING_KEY: &[u8] = b"a sufficiently long test signing key";

fn claims() -> FetchClaims {
    FetchClaims {
        protocol_version: FETCH_PROTOCOL_VERSION,
        policy_version: "policy-v1".to_string(),
        namespace: "namespace-a".to_string(),
        run_id: "run-a".to_string(),
        command_id: "command-a".to_string(),
        issued_at_unix: 1_000,
        expires_at_unix: 1_060,
        max_concurrency: 2,
        max_requests: 20,
        max_request_bytes: 8 * 1024 * 1024,
        max_response_bytes: 32 * 1024 * 1024,
    }
}

fn caps() -> BrokerAuthCaps {
    BrokerAuthCaps {
        protocol_version: FETCH_PROTOCOL_VERSION,
        policy_version: "policy-v1".to_string(),
        max_concurrency: 2,
        max_requests: 20,
        max_request_bytes: 8 * 1024 * 1024,
        max_response_bytes: 32 * 1024 * 1024,
        max_future_iat: Duration::from_secs(5),
    }
}

fn at(seconds: u64) -> std::time::SystemTime {
    UNIX_EPOCH + Duration::from_secs(seconds)
}

fn verifier() -> TokenVerifier {
    TokenVerifier::new(SIGNING_KEY, caps()).unwrap()
}

fn verified_claims() -> agent_runtime::fetch_auth::VerifiedClaims {
    let issuer = TokenIssuer::new(SIGNING_KEY).unwrap();
    verifier()
        .verify(&issuer.issue(&claims()).unwrap(), at(1_001))
        .unwrap()
}

#[test]
fn token_round_trip_binds_identity_and_rejects_tampering() {
    let issuer = TokenIssuer::new(SIGNING_KEY).unwrap();
    let verifier = verifier();
    let token = issuer.issue(&claims()).unwrap();
    let verified = verifier
        .verify_for(
            &token,
            UNIX_EPOCH + Duration::from_secs(1_001),
            &CommandIdentity::new("namespace-a", "run-a", "command-a"),
        )
        .unwrap();
    assert_eq!(verified.claims(), &claims());

    let mut tampered = token.expose_secret().to_string();
    let last = tampered.pop().unwrap();
    tampered.push(if last == 'A' { 'B' } else { 'A' });
    let error = verifier
        .verify(&SecretString::new(tampered), at(1_001))
        .unwrap_err();
    assert_eq!(error.kind(), AuthErrorKind::InvalidSignature);
}

#[test]
fn issued_tokens_use_canonical_unpadded_base64url() {
    let token = TokenIssuer::new(SIGNING_KEY)
        .unwrap()
        .issue(&claims())
        .unwrap();
    let parts: Vec<_> = token.expose_secret().split('.').collect();

    assert_eq!(parts.len(), 3);
    assert_eq!(parts[0], "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9");
    assert!(parts.iter().all(|part| !part.contains('=')));
}

#[test]
fn expired_token_is_rejected() {
    let issuer = TokenIssuer::new(SIGNING_KEY).unwrap();
    let mut expired = claims();
    expired.expires_at_unix = 1_001;

    let error = verifier()
        .verify(&issuer.issue(&expired).unwrap(), at(1_001))
        .unwrap_err();
    assert_eq!(error.kind(), AuthErrorKind::Expired);
}

#[test]
fn future_issued_at_is_rejected() {
    let issuer = TokenIssuer::new(SIGNING_KEY).unwrap();
    let mut future = claims();
    future.issued_at_unix = 1_007;

    let error = verifier()
        .verify(&issuer.issue(&future).unwrap(), at(1_001))
        .unwrap_err();
    assert_eq!(error.kind(), AuthErrorKind::IssuedInFuture);
}

#[test]
fn protocol_and_policy_mismatches_are_rejected() {
    let issuer = TokenIssuer::new(SIGNING_KEY).unwrap();
    let mut protocol_mismatch = claims();
    protocol_mismatch.protocol_version += 1;
    let error = verifier()
        .verify(&issuer.issue(&protocol_mismatch).unwrap(), at(1_001))
        .unwrap_err();
    assert_eq!(error.kind(), AuthErrorKind::ProtocolVersionMismatch);

    let mut policy_mismatch = claims();
    policy_mismatch.policy_version = "policy-v2".to_string();
    let error = verifier()
        .verify(&issuer.issue(&policy_mismatch).unwrap(), at(1_001))
        .unwrap_err();
    assert_eq!(error.kind(), AuthErrorKind::PolicyVersionMismatch);
}

#[test]
fn verify_for_rejects_namespace_run_and_command_mismatches() {
    let issuer = TokenIssuer::new(SIGNING_KEY).unwrap();
    let token = issuer.issue(&claims()).unwrap();
    for identity in [
        CommandIdentity::new("namespace-b", "run-a", "command-a"),
        CommandIdentity::new("namespace-a", "run-b", "command-a"),
        CommandIdentity::new("namespace-a", "run-a", "command-b"),
    ] {
        let error = verifier()
            .verify_for(&token, at(1_001), &identity)
            .unwrap_err();
        assert_eq!(error.kind(), AuthErrorKind::IdentityMismatch);
    }
}

#[test]
fn claims_above_each_broker_cap_are_rejected() {
    let issuer = TokenIssuer::new(SIGNING_KEY).unwrap();
    let mut over_concurrency = claims();
    over_concurrency.max_concurrency = 3;
    let mut over_requests = claims();
    over_requests.max_requests = 21;
    let mut over_request_bytes = claims();
    over_request_bytes.max_request_bytes += 1;
    let mut over_response_bytes = claims();
    over_response_bytes.max_response_bytes += 1;

    for oversized in [
        over_concurrency,
        over_requests,
        over_request_bytes,
        over_response_bytes,
    ] {
        let error = verifier()
            .verify(&issuer.issue(&oversized).unwrap(), at(1_001))
            .unwrap_err();
        assert_eq!(error.kind(), AuthErrorKind::ClaimsExceedBrokerCaps);
    }
}

#[test]
fn lower_claims_are_the_effective_limits() {
    let issuer = TokenIssuer::new(SIGNING_KEY).unwrap();
    let mut lower = claims();
    lower.max_concurrency = 1;
    lower.max_requests = 10;
    lower.max_request_bytes = 1024;
    lower.max_response_bytes = 2048;

    let verified = verifier()
        .verify(&issuer.issue(&lower).unwrap(), at(1_001))
        .unwrap();
    assert_eq!(verified.effective_limits.max_concurrency, 1);
    assert_eq!(verified.effective_limits.max_requests, 10);
    assert_eq!(verified.effective_limits.max_request_bytes, 1024);
    assert_eq!(verified.effective_limits.max_response_bytes, 2048);
}

#[test]
fn quota_denies_the_twenty_first_request() {
    let registry = QuotaRegistry::new(Duration::from_secs(10));
    let claims = verified_claims();
    for _ in 0..20 {
        drop(registry.acquire_at(&claims, at(1_001)).unwrap());
    }

    let error = registry.acquire_at(&claims, at(1_001)).unwrap_err();
    assert_eq!(error.kind(), QuotaErrorKind::RequestLimitReached);
}

#[test]
fn quota_denies_a_third_concurrent_request_and_lease_drop_releases_capacity() {
    let registry = QuotaRegistry::new(Duration::from_secs(10));
    let claims = verified_claims();
    let first = registry.acquire_at(&claims, at(1_001)).unwrap();
    let second = registry.acquire_at(&claims, at(1_001)).unwrap();
    assert_eq!((first.requests_used(), first.concurrent_requests()), (1, 1));
    assert_eq!(
        (second.requests_used(), second.concurrent_requests()),
        (2, 2)
    );

    let error = registry.acquire_at(&claims, at(1_001)).unwrap_err();
    assert_eq!(error.kind(), QuotaErrorKind::ConcurrencyLimitReached);

    drop(first);
    let replacement = registry.acquire_at(&claims, at(1_001)).unwrap();
    assert_eq!(replacement.requests_used(), 3);
    assert_eq!(replacement.concurrent_requests(), 2);
    drop(replacement);
    drop(second);
}

#[test]
fn quota_cleanup_retains_expired_identity_for_its_cleanup_window() {
    let registry = QuotaRegistry::new(Duration::from_secs(10));
    let claims = verified_claims();
    drop(registry.acquire_at(&claims, at(1_001)).unwrap());

    assert_eq!(registry.cleanup_at(at(1_070)), 0);
    assert_eq!(registry.entry_count_at(at(1_070)), 1);
    assert_eq!(registry.cleanup_at(at(1_071)), 1);
    assert_eq!(registry.entry_count_at(at(1_071)), 0);
}

#[test]
fn token_secrets_do_not_appear_in_debug_or_errors() {
    let secret = "a sufficiently long test signing key";
    let issuer = TokenIssuer::new(secret).unwrap();
    let verifier = verifier();
    let error = verifier
        .verify(&SecretString::new("invalid"), at(1_001))
        .unwrap_err();

    assert!(!format!("{issuer:?}").contains(secret));
    assert!(!format!("{verifier:?}").contains(secret));
    assert!(!format!("{error:?}").contains(secret));
    assert!(!error.to_string().contains(secret));
}
