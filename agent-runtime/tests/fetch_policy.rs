use agent_runtime::fetch_policy::{
    BodyReplay, BudgetTracker, HeaderPolicy, NeedsFreshResolution, PolicyCode, PolicyConfig,
    RedirectPolicy, TargetHost, TargetPolicy,
};
use http::{Method, StatusCode};
use ipnet::IpNet;
use std::net::{IpAddr, SocketAddr};

const EXPECTED_DEFAULT_USER_AGENT: &str = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36";

fn defaults() -> PolicyConfig {
    PolicyConfig::approved_defaults()
}

fn targets() -> TargetPolicy {
    TargetPolicy::new(defaults())
}

fn headers() -> HeaderPolicy {
    HeaderPolicy::new(defaults())
}

fn redirects() -> RedirectPolicy {
    RedirectPolicy::new(defaults())
}

fn raw_headers(values: &[(&str, &str)]) -> Vec<(String, String)> {
    values
        .iter()
        .map(|(name, value)| ((*name).to_string(), (*value).to_string()))
        .collect()
}

#[test]
fn approved_defaults_are_exact() {
    let config = defaults();
    assert_eq!(config.request_header_bytes, 32 * 1024);
    assert_eq!(config.request_body_bytes, 8 * 1024 * 1024);
    assert_eq!(config.response_header_bytes, 32 * 1024);
    assert_eq!(config.response_network_bytes, 16 * 1024 * 1024);
    assert_eq!(config.response_decoded_bytes, 32 * 1024 * 1024);
    assert_eq!(config.max_decompression_ratio, 20);
    assert_eq!(config.max_redirects, 5);
    assert!(config.deny_cidrs.is_empty());
    assert!(config.credential_header_names.is_empty());
}

#[test]
fn canonical_public_hosts_and_literal_addresses_are_normalized_once() {
    let policy = targets();
    let domain = policy
        .normalize("HTTPS://PUBLIC.Example/data?q=one%20two")
        .unwrap();
    assert_eq!(
        domain.url.as_str(),
        "https://public.example/data?q=one%20two"
    );
    assert_eq!(domain.port, 443);
    assert_eq!(domain.host, TargetHost::Name("public.example".to_string()));
    assert_eq!(domain.resolution, NeedsFreshResolution);

    let ipv4 = policy.normalize("http://93.184.216.34:8080/index").unwrap();
    assert_eq!(ipv4.port, 8080);
    assert_eq!(
        ipv4.host,
        TargetHost::Address("93.184.216.34".parse().unwrap())
    );

    let ipv6 = policy
        .normalize("https://[2606:2800:220:1:248:1893:25c8:1946]/")
        .unwrap();
    assert_eq!(
        ipv6.host,
        TargetHost::Address("2606:2800:220:1:248:1893:25c8:1946".parse().unwrap())
    );
}

#[test]
fn ambiguous_or_noncanonical_urls_are_rejected_before_parser_reinterpretation() {
    let policy = targets();
    let rejected = [
        "ftp://public.example/",
        "https://user@public.example/",
        "https://public.example/#fragment",
        "https://public.example/with space",
        "https://public.example/\nnext",
        "https:\\public.example\\admin",
        "https://",
        "https:///missing.example",
        "https://localhost/",
        "https://public。example/",
        "https://public%2eexample/",
        "http://public.example:80/",
        "https://public.example:443/",
        "https://public.example:0/",
        "https://public.example:0443/",
        "https://public.example:65536/",
        "https://2130706433/",
        "https://0x7f000001/",
        "https://0177.0.0.1/",
        "https://127.000.000.001/",
        "https://[2001:0DB8::1]/",
        "https://[2001:0db8:0:0:0:0:0:1]/",
        "https://[fe80::1%25eth0]/",
        "https://public.example/%252e%252e/admin",
        "https://public.example/%25%32%65/admin",
        "https://-bad.example/",
        "https://bad-.example/",
        "https://bad..example/",
    ];
    for raw in rejected {
        assert!(
            policy.normalize(raw).is_err(),
            "unexpectedly accepted {raw}"
        );
    }
}

#[test]
fn complete_public_answers_are_sorted_deduplicated_and_keep_the_original_port() {
    let policy = targets();
    let reviewed = policy
        .normalize("https://public.example:8443/data")
        .unwrap();
    let public_v4: IpAddr = "93.184.216.34".parse().unwrap();
    let public_v6: IpAddr = "2606:2800:220:1:248:1893:25c8:1946".parse().unwrap();
    let approved = policy
        .review_answers(reviewed, &[public_v6, public_v4, public_v4])
        .unwrap();
    assert_eq!(
        approved.addresses,
        vec![
            SocketAddr::new(public_v4, 8443),
            SocketAddr::new(public_v6, 8443),
        ]
    );
}

#[test]
fn empty_mixed_cname_and_rebinding_answers_fail_closed() {
    let policy = targets();
    let target = || policy.normalize("https://public.example/data").unwrap();
    assert_eq!(
        policy.review_answers(target(), &[]).unwrap_err().code(),
        PolicyCode::EmptyAnswers
    );
    assert_eq!(
        policy
            .review_answers(
                target(),
                &[
                    "93.184.216.34".parse().unwrap(),
                    "fd00::10".parse().unwrap(),
                ],
            )
            .unwrap_err()
            .code(),
        PolicyCode::RestrictedAddress
    );

    let cname_final_private = ["10.0.0.8".parse().unwrap()];
    assert_eq!(
        policy
            .review_answers(target(), &cname_final_private)
            .unwrap_err()
            .code(),
        PolicyCode::RestrictedAddress
    );

    let first = policy
        .review_answers(target(), &["93.184.216.34".parse().unwrap()])
        .unwrap();
    let retry = first.into_retry();
    assert_eq!(retry.resolution, NeedsFreshResolution);
    assert_eq!(
        policy
            .review_answers(retry.target, &["127.0.0.1".parse().unwrap()])
            .unwrap_err()
            .code(),
        PolicyCode::RestrictedAddress
    );
}

#[test]
fn every_builtin_non_public_address_class_is_rejected() {
    let policy = targets();
    let restricted = [
        "0.0.0.0",
        "10.0.0.1",
        "100.64.0.1",
        "127.0.0.1",
        "169.254.169.254",
        "172.16.0.1",
        "192.0.0.1",
        "192.0.2.1",
        "192.88.99.1",
        "192.168.1.1",
        "198.18.0.1",
        "198.51.100.1",
        "203.0.113.1",
        "224.0.0.1",
        "255.255.255.255",
        "::",
        "::1",
        "fc00::1",
        "fd00:ec2::254",
        "fe80::1",
        "ff02::1",
        "2001:2::1",
        "2001:100::1",
        "2001:db8::1",
        "3fff::1",
        "::ffff:127.0.0.1",
        "64:ff9b::7f00:1",
        "64:ff9b:1:a00:0:100::",
        "2002:0a00:0001::",
        "2001:0000:4136:e378:8000:63bf:3f57:fefe",
    ];
    for raw in restricted {
        let reviewed = policy.normalize("https://public.example/").unwrap();
        let answer = raw.parse().unwrap();
        assert_eq!(
            policy
                .review_answers(reviewed, &[answer])
                .unwrap_err()
                .code(),
            PolicyCode::RestrictedAddress,
            "unexpectedly accepted {raw}"
        );
    }
}

#[test]
fn canonical_globally_reachable_and_publicly_embedded_addresses_are_allowed() {
    let policy = targets();
    let public = [
        "1.1.1.1",
        "8.8.8.8",
        "93.184.216.34",
        "192.0.0.9",
        "192.0.0.10",
        "192.88.99.2",
        "2001:4860:4860::8888",
        "2606:4700:4700::1111",
        "64:ff9b::5db8:d822",
        "2002:5db8:d822::",
        "2001:0000:4136:e378:8000:63bf:a24f:27dd",
    ];
    for raw in public {
        let target = policy.normalize("https://public.example/").unwrap();
        policy
            .review_answers(target, &[raw.parse().unwrap()])
            .unwrap_or_else(|error| panic!("rejected public address {raw}: {error}"));
    }
}

#[test]
fn configured_deployment_cidrs_are_denied_for_both_families() {
    let mut config = defaults();
    config.deny_cidrs = vec![
        "93.184.216.0/24".parse::<IpNet>().unwrap(),
        "2606:2800:220::/48".parse::<IpNet>().unwrap(),
    ];
    let policy = TargetPolicy::new(config);
    for answer in ["93.184.216.34", "2606:2800:220:1::1"] {
        let reviewed = policy.normalize("https://public.example/").unwrap();
        assert_eq!(
            policy
                .review_answers(reviewed, &[answer.parse().unwrap()])
                .unwrap_err()
                .code(),
            PolicyCode::RestrictedAddress
        );
    }

    let reviewed = policy.normalize("https://public.example/").unwrap();
    assert_eq!(
        policy
            .review_answers(reviewed, &["64:ff9b::5db8:d822".parse().unwrap()])
            .unwrap_err()
            .code(),
        PolicyCode::RestrictedAddress
    );
}

#[test]
fn literal_targets_must_be_public_and_answers_must_match_the_literal() {
    let policy = targets();
    let private = policy.normalize("http://127.0.0.1/").unwrap();
    assert_eq!(
        policy
            .review_answers(private, &["127.0.0.1".parse().unwrap()])
            .unwrap_err()
            .code(),
        PolicyCode::RestrictedAddress
    );

    let public = policy.normalize("https://93.184.216.34/").unwrap();
    assert_eq!(
        policy
            .review_answers(public, &["1.1.1.1".parse().unwrap()])
            .unwrap_err()
            .code(),
        PolicyCode::AnswerMismatch
    );
}

#[test]
fn application_headers_are_allowed_and_sensitive_names_are_case_insensitive() {
    let mut config = defaults();
    config
        .credential_header_names
        .push("X-Tenant-Secret".to_string());
    let reviewed = HeaderPolicy::new(config)
        .review(&raw_headers(&[
            ("Authorization", "Bearer secret"),
            ("Cookie", "session=secret"),
            ("X-API-Key", "key"),
            ("x-tenant-secret", "tenant"),
            ("X-Application", "allowed"),
        ]))
        .unwrap();
    assert_eq!(reviewed.headers.len(), 6);
    assert!(reviewed.is_sensitive("AUTHORIZATION"));
    assert!(reviewed.is_sensitive("cookie"));
    assert!(reviewed.is_sensitive("x-api-key"));
    assert!(reviewed.is_sensitive("X-TENANT-SECRET"));
    assert!(!reviewed.is_sensitive("x-application"));
}

#[test]
fn header_policy_injects_exact_default_user_agent() {
    let reviewed = headers()
        .review(&raw_headers(&[("X-Application", "allowed")]))
        .unwrap();

    assert_eq!(
        reviewed.headers.get("user-agent").unwrap(),
        EXPECTED_DEFAULT_USER_AGENT
    );
    assert!(reviewed.headers.contains_key("x-application"));
}

#[test]
fn caller_user_agent_suppresses_default_case_insensitively() {
    let reviewed = headers()
        .review(&raw_headers(&[("uSeR-aGeNt", "caller")]))
        .unwrap();

    assert_eq!(reviewed.headers.get("user-agent").unwrap(), "caller");
    assert_eq!(reviewed.headers.get_all("user-agent").iter().count(), 1);
}

#[test]
fn duplicate_caller_user_agent_uses_last_value_only() {
    let reviewed = headers()
        .review(&raw_headers(&[
            ("User-Agent", "first"),
            ("user-agent", "second"),
        ]))
        .unwrap();

    assert_eq!(reviewed.headers.get("user-agent").unwrap(), "second");
    assert_eq!(reviewed.headers.get_all("user-agent").iter().count(), 1);
}

#[test]
fn non_user_agent_duplicate_semantics_are_unchanged() {
    let reviewed = headers()
        .review(&raw_headers(&[
            ("X-Repeat", "first"),
            ("x-repeat", "second"),
        ]))
        .unwrap();

    let values = reviewed
        .headers
        .get_all("x-repeat")
        .iter()
        .map(|value| value.to_str().unwrap())
        .collect::<Vec<_>>();
    assert_eq!(values, ["first", "second"]);
}

#[test]
fn final_map_budget_includes_default_or_winning_user_agent() {
    let default_bytes = ("user-agent".len() + 2 + EXPECTED_DEFAULT_USER_AGENT.len() + 2) as u64;
    let mut default_budget = defaults();
    default_budget.request_header_bytes = default_bytes - 1;
    assert_eq!(
        HeaderPolicy::new(default_budget)
            .review(&[])
            .unwrap_err()
            .code(),
        PolicyCode::BudgetExceeded
    );

    let mut winning_budget = defaults();
    winning_budget.request_header_bytes = ("user-agent".len() + 2 + 1 + 2) as u64;
    let reviewed = HeaderPolicy::new(winning_budget)
        .review(&raw_headers(&[
            ("User-Agent", "discarded"),
            ("USER-AGENT", "x"),
        ]))
        .unwrap();
    assert_eq!(reviewed.wire_bytes, ("user-agent".len() + 2 + 1 + 2) as u64);
    assert_eq!(reviewed.headers.get("user-agent").unwrap(), "x");
}

#[test]
fn transport_and_proxy_headers_are_rejected() {
    let policy = headers();
    let denied = [
        "Host",
        "Content-Length",
        "Transfer-Encoding",
        "Connection",
        "Upgrade",
        "TE",
        "Trailer",
        "Proxy-Authorization",
        "Proxy-Authenticate",
        "Proxy-Connection",
        "Keep-Alive",
        "Forwarded",
        "X-Forwarded-For",
        "X-Forwarded-Host",
        "X-Forwarded-Proto",
        "Via",
        "X-Real-IP",
    ];
    for name in denied {
        assert_eq!(
            policy
                .review(&raw_headers(&[(name, "value")]))
                .unwrap_err()
                .code(),
            PolicyCode::ForbiddenHeader,
            "unexpectedly accepted {name}"
        );
    }
}

#[test]
fn header_tokens_values_and_exact_wire_size_are_enforced() {
    let policy = headers();
    for values in [
        raw_headers(&[("bad header", "value")]),
        raw_headers(&[("x-test", "good\r\ninjected: true")]),
        raw_headers(&[("x-test", "bad\0value")]),
    ] {
        assert_eq!(
            policy.review(&values).unwrap_err().code(),
            PolicyCode::InvalidHeader
        );
    }

    let mut config = defaults();
    config.request_header_bytes = 25;
    let policy = HeaderPolicy::new(config);
    let exact = policy
        .review(&raw_headers(&[("x", "value"), ("User-Agent", "a")]))
        .unwrap();
    assert_eq!(exact.wire_bytes, 25);
    assert_eq!(
        policy
            .review(&raw_headers(&[("x", "values"), ("User-Agent", "a")]))
            .unwrap_err()
            .code(),
        PolicyCode::BudgetExceeded
    );
}

#[test]
fn redirects_resolve_relative_locations_and_always_require_fresh_resolution() {
    let current = targets().normalize("https://a.example/one/start").unwrap();
    let reviewed_headers = headers()
        .review(&raw_headers(&[("X-Application", "allowed")]))
        .unwrap();
    let next = redirects()
        .review(
            &current,
            StatusCode::FOUND,
            "../next",
            reviewed_headers,
            Method::GET,
            BodyReplay::Empty,
            0,
        )
        .unwrap();
    assert_eq!(next.target.url.as_str(), "https://a.example/next");
    assert_eq!(next.resolution, NeedsFreshResolution);
    assert_eq!(next.hops, 1);
}

#[test]
fn redirects_strip_all_cross_origin_credentials_but_preserve_same_origin_headers() {
    let mut config = defaults();
    config
        .credential_header_names
        .push("X-Tenant-Secret".to_string());
    let target_policy = TargetPolicy::new(config.clone());
    let header_policy = HeaderPolicy::new(config.clone());
    let redirect_policy = RedirectPolicy::new(config);
    let current = target_policy.normalize("https://a.example/start").unwrap();
    let make_headers = || {
        header_policy
            .review(&raw_headers(&[
                ("Authorization", "Bearer secret"),
                ("Cookie", "session=secret"),
                ("X-API-Key", "key"),
                ("X-Tenant-Secret", "tenant"),
                ("X-Application", "allowed"),
            ]))
            .unwrap()
    };

    let same = redirect_policy
        .review(
            &current,
            StatusCode::TEMPORARY_REDIRECT,
            "/same",
            make_headers(),
            Method::POST,
            BodyReplay::Replayable { bytes: 12 },
            0,
        )
        .unwrap();
    assert_eq!(same.headers.headers.len(), 6);

    let cross = redirect_policy
        .review(
            &current,
            StatusCode::TEMPORARY_REDIRECT,
            "https://b.example/next",
            make_headers(),
            Method::POST,
            BodyReplay::Replayable { bytes: 12 },
            0,
        )
        .unwrap();
    assert!(!cross.headers.contains("authorization"));
    assert!(!cross.headers.contains("cookie"));
    assert!(!cross.headers.contains("x-api-key"));
    assert!(!cross.headers.contains("x-tenant-secret"));
    assert!(cross.headers.contains("x-application"));
    assert_eq!(cross.body, BodyReplay::Replayable { bytes: 12 });
}

#[test]
fn redirects_preserve_user_agent_while_stripping_credentials() {
    let mut config = defaults();
    config
        .credential_header_names
        .push("X-Tenant-Secret".to_string());
    let target_policy = TargetPolicy::new(config.clone());
    let header_policy = HeaderPolicy::new(config.clone());
    let redirect_policy = RedirectPolicy::new(config);
    let current = target_policy.normalize("https://a.example/start").unwrap();
    let reviewed = header_policy
        .review(&raw_headers(&[
            ("Authorization", "Bearer secret"),
            ("X-Tenant-Secret", "tenant"),
        ]))
        .unwrap();

    let next = redirect_policy
        .review(
            &current,
            StatusCode::FOUND,
            "https://b.example/next",
            reviewed,
            Method::GET,
            BodyReplay::Empty,
            0,
        )
        .unwrap();
    assert!(!next.headers.contains("authorization"));
    assert!(!next.headers.contains("x-tenant-secret"));
    assert_eq!(
        next.headers.headers.get("user-agent").unwrap(),
        EXPECTED_DEFAULT_USER_AGENT
    );
    assert!(!next.headers.is_sensitive("user-agent"));
}

#[test]
fn redirect_policy_applies_its_own_admin_sensitive_names() {
    let current = targets().normalize("https://a.example/start").unwrap();
    let reviewed_headers = headers()
        .review(&raw_headers(&[
            ("X-Deployment-Credential", "secret"),
            ("X-Application", "allowed"),
        ]))
        .unwrap();
    assert!(!reviewed_headers.is_sensitive("x-deployment-credential"));

    let mut config = defaults();
    config
        .credential_header_names
        .push("X-Deployment-Credential".to_string());
    let next = RedirectPolicy::new(config)
        .review(
            &current,
            StatusCode::FOUND,
            "https://b.example/next",
            reviewed_headers,
            Method::GET,
            BodyReplay::Empty,
            0,
        )
        .unwrap();
    assert!(!next.headers.contains("x-deployment-credential"));
    assert!(next.headers.contains("x-application"));
}

#[test]
fn redirect_limits_downgrades_and_raw_location_ambiguities_are_rejected() {
    let current = targets().normalize("https://a.example/start").unwrap();
    let call = |location: &str, hops: u8| {
        redirects().review(
            &current,
            StatusCode::FOUND,
            location,
            headers().review(&[]).unwrap(),
            Method::GET,
            BodyReplay::Empty,
            hops,
        )
    };
    assert_eq!(
        call("http://a.example/insecure", 0).unwrap_err().code(),
        PolicyCode::HttpsDowngrade
    );
    assert_eq!(
        call("https://b.example/sixth", 5).unwrap_err().code(),
        PolicyCode::TooManyRedirects
    );
    for location in ["https://user@b.example/", "//b%2eexample/", "/%252e%252e"] {
        assert!(call(location, 0).is_err(), "accepted redirect {location}");
    }
}

#[test]
fn redirect_method_and_body_replay_semantics_are_explicit() {
    let current = targets().normalize("https://a.example/start").unwrap();
    let review = |status, method, body| {
        redirects().review(
            &current,
            status,
            "/next",
            headers().review(&[]).unwrap(),
            method,
            body,
            0,
        )
    };

    for status in [StatusCode::MOVED_PERMANENTLY, StatusCode::FOUND] {
        let next = review(
            status,
            Method::POST,
            BodyReplay::NonReplayable { bytes: Some(12) },
        )
        .unwrap();
        assert_eq!(next.method, Method::GET);
        assert_eq!(next.body, BodyReplay::Empty);
    }
    let see_other = review(
        StatusCode::SEE_OTHER,
        Method::PUT,
        BodyReplay::NonReplayable { bytes: Some(12) },
    )
    .unwrap();
    assert_eq!(see_other.method, Method::GET);
    assert_eq!(see_other.body, BodyReplay::Empty);

    for status in [
        StatusCode::TEMPORARY_REDIRECT,
        StatusCode::PERMANENT_REDIRECT,
    ] {
        assert_eq!(
            review(
                status,
                Method::POST,
                BodyReplay::NonReplayable { bytes: Some(12) },
            )
            .unwrap_err()
            .code(),
            PolicyCode::BodyNotReplayable
        );
        let replayed = review(status, Method::POST, BodyReplay::Replayable { bytes: 12 }).unwrap();
        assert_eq!(replayed.method, Method::POST);
        assert_eq!(replayed.body, BodyReplay::Replayable { bytes: 12 });
    }
}

#[test]
fn budget_tracker_enforces_every_limit_at_chunk_and_header_boundaries() {
    let config = defaults();
    let mut budget = BudgetTracker::new(config.clone());
    budget.record_request_header("x-test", "value").unwrap();
    budget
        .record_response_header("content-type", "text/plain")
        .unwrap();
    budget
        .record_request_body_chunk(config.request_body_bytes)
        .unwrap();
    assert_eq!(
        budget.record_request_body_chunk(1).unwrap_err().code(),
        PolicyCode::BudgetExceeded
    );

    let mut network = BudgetTracker::new(config.clone());
    network
        .record_response_network_chunk(config.response_network_bytes)
        .unwrap();
    assert_eq!(
        network.record_response_network_chunk(1).unwrap_err().code(),
        PolicyCode::BudgetExceeded
    );

    let mut decoded = BudgetTracker::new(config.clone());
    decoded
        .record_response_network_chunk(2 * 1024 * 1024)
        .unwrap();
    decoded
        .record_response_decoded_chunk(config.response_decoded_bytes)
        .unwrap();
    assert_eq!(
        decoded.record_response_decoded_chunk(1).unwrap_err().code(),
        PolicyCode::BudgetExceeded
    );

    let declared = BudgetTracker::new(config);
    assert_eq!(
        declared
            .check_request_body_length(8 * 1024 * 1024 + 1)
            .unwrap_err()
            .code(),
        PolicyCode::BudgetExceeded
    );
}

#[test]
fn response_header_wire_budget_is_checked_per_header() {
    let mut config = defaults();
    config.response_header_bytes = 10;
    let mut budget = BudgetTracker::new(config);
    budget.record_response_header("x", "value").unwrap();
    assert_eq!(budget.response_header_bytes(), 10);
    assert_eq!(
        budget
            .record_response_header("y", "value")
            .unwrap_err()
            .code(),
        PolicyCode::BudgetExceeded
    );
}

#[test]
fn decompression_ratio_zero_network_and_checked_arithmetic_fail_closed() {
    let mut ratio = BudgetTracker::new(defaults());
    assert_eq!(
        ratio.record_response_decoded_chunk(1).unwrap_err().code(),
        PolicyCode::DecompressionRatioExceeded
    );

    let mut ratio = BudgetTracker::new(defaults());
    ratio.record_response_network_chunk(1).unwrap();
    ratio.record_response_decoded_chunk(20).unwrap();
    assert_eq!(
        ratio.record_response_decoded_chunk(1).unwrap_err().code(),
        PolicyCode::DecompressionRatioExceeded
    );

    let mut config = defaults();
    config.request_body_bytes = u64::MAX;
    let mut overflow = BudgetTracker::new(config);
    overflow.record_request_body_chunk(u64::MAX).unwrap();
    assert_eq!(
        overflow.record_request_body_chunk(1).unwrap_err().code(),
        PolicyCode::ArithmeticOverflow
    );
}
