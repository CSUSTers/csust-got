use agent_runtime::{
    config::{RuntimeConfig, RuntimeFetchConfig},
    fetch_broker::BrokerConfig,
    fetch_protocol::FETCH_PROTOCOL_VERSION,
};
use std::{collections::HashMap, net::SocketAddr, path::PathBuf, time::Duration};

fn absolute(name: &str) -> String {
    std::env::temp_dir()
        .join("agent-runtime-config-tests")
        .join(name)
        .to_string_lossy()
        .into_owned()
}

fn runtime_env() -> HashMap<&'static str, String> {
    HashMap::from([
        ("AGENT_RUNTIME_CGROUP_AGGREGATE_ROOT", absolute("aggregate")),
        ("AGENT_RUNTIME_CGROUP_ROOT", absolute("cgroup")),
        (
            "AGENT_RUNTIME_WORKSPACE_MAX_BYTES",
            (512_u64 * 1024 * 1024).to_string(),
        ),
        ("AGENT_RUNTIME_TOKEN", "runtime-secret".to_string()),
    ])
}

fn parse_runtime(values: &HashMap<&str, String>) -> Result<RuntimeConfig, String> {
    RuntimeConfig::from_env(|name| values.get(name).cloned()).map_err(|error| error.to_string())
}

#[test]
fn runtime_config_has_exact_approved_defaults() {
    let config = parse_runtime(&runtime_env()).unwrap();

    assert_eq!(
        config.listen_addr,
        "0.0.0.0:8080".parse::<SocketAddr>().unwrap()
    );
    assert_eq!(config.workspace_root, PathBuf::from("workspaces"));
    assert_eq!(config.skills_root, PathBuf::from("skills"));
    assert_eq!(config.workspace_max_bytes, 512 * 1024 * 1024);
    assert_eq!(config.cgroup.limits.pids_max, 64);
    assert_eq!(config.cgroup.limits.memory_max_bytes, 256 * 1024 * 1024);
    assert_eq!(config.cgroup.limits.memory_swap_max_bytes, 0);
    assert_eq!(config.cgroup.limits.cpu_quota_us, 100_000);
    assert_eq!(config.cgroup.limits.cpu_period_us, 100_000);
    assert_eq!(config.cgroup.limits.cpu_budget, Duration::from_secs(120));
    assert_eq!(config.rlimits.nproc, 480);
    assert_eq!(config.rlimits.nofile, 256);
    assert_eq!(config.rlimits.fsize_bytes, 64 * 1024 * 1024);
    assert_eq!(config.rlimits.core_bytes, 0);
    assert_eq!(config.command_timeout, Duration::from_secs(120));
    assert!(matches!(config.fetch, RuntimeFetchConfig::Disabled));
}

#[test]
fn runtime_config_requires_every_unbounded_security_input() {
    for name in [
        "AGENT_RUNTIME_CGROUP_AGGREGATE_ROOT",
        "AGENT_RUNTIME_CGROUP_ROOT",
        "AGENT_RUNTIME_WORKSPACE_MAX_BYTES",
        "AGENT_RUNTIME_TOKEN",
    ] {
        let mut missing = runtime_env();
        missing.remove(name);
        let error = parse_runtime(&missing).unwrap_err();
        assert!(error.contains(name), "{name}: {error}");

        let mut blank = runtime_env();
        blank.insert(name, " \t ".to_string());
        let error = parse_runtime(&blank).unwrap_err();
        assert!(error.contains(name), "{name}: {error}");
    }
}

#[test]
fn disabled_fetch_does_not_read_broker_configuration() {
    let mut values = runtime_env();
    values.insert("AGENT_FETCH_SOCKET", "relative.sock".to_string());
    values.insert("AGENT_FETCH_HMAC_KEY_FILE", "relative.key".to_string());
    values.insert("AGENT_FETCH_POLICY_VERSION", "".to_string());

    assert!(matches!(
        parse_runtime(&values).unwrap().fetch,
        RuntimeFetchConfig::Disabled
    ));
}

#[test]
fn enabled_fetch_requires_and_bounds_private_broker_configuration() {
    let mut values = runtime_env();
    values.insert("AGENT_RUNTIME_FETCH_ENABLED", "true".to_string());
    values.insert("AGENT_FETCH_SOCKET", absolute("fetch.sock"));
    values.insert("AGENT_FETCH_HMAC_KEY_FILE", absolute("fetch.key"));
    values.insert("AGENT_FETCH_POLICY_VERSION", "policy-v1".to_string());
    let config = parse_runtime(&values).unwrap();
    let RuntimeFetchConfig::Enabled { limits, .. } = config.fetch else {
        panic!("fetch should be enabled");
    };
    assert_eq!(limits.protocol_version, FETCH_PROTOCOL_VERSION);
    assert_eq!(limits.max_concurrency, 2);
    assert_eq!(limits.max_requests, 20);

    values.remove("AGENT_FETCH_SOCKET");
    assert!(
        parse_runtime(&values)
            .unwrap_err()
            .contains("AGENT_FETCH_SOCKET")
    );
}

#[test]
fn runtime_config_rejects_zero_overflow_and_malformed_numbers() {
    for name in [
        "AGENT_RUNTIME_WORKSPACE_MAX_BYTES",
        "AGENT_RUNTIME_COMMAND_PIDS_MAX",
        "AGENT_RUNTIME_COMMAND_MEMORY_MAX_BYTES",
        "AGENT_RUNTIME_COMMAND_CPU_QUOTA_US",
        "AGENT_RUNTIME_COMMAND_CPU_PERIOD_US",
        "AGENT_RUNTIME_COMMAND_CPU_BUDGET_SECS",
        "AGENT_RUNTIME_COMMAND_NPROC",
        "AGENT_RUNTIME_COMMAND_NOFILE",
        "AGENT_RUNTIME_COMMAND_FSIZE_BYTES",
        "AGENT_RUNTIME_COMMAND_TIMEOUT_SECS",
    ] {
        for invalid in ["", "0", "-1", "1s", "18446744073709551616"] {
            let mut values = runtime_env();
            values.insert(name, invalid.to_string());
            let error = parse_runtime(&values).unwrap_err();
            assert!(error.contains(name), "{name}={invalid:?}: {error}");
        }
    }

    for name in [
        "AGENT_RUNTIME_FETCH_MAX_CONCURRENCY",
        "AGENT_RUNTIME_FETCH_MAX_REQUESTS",
        "AGENT_RUNTIME_FETCH_MAX_REQUEST_BYTES",
        "AGENT_RUNTIME_FETCH_MAX_RESPONSE_BYTES",
    ] {
        for invalid in ["", "0", "-1", "1s", "18446744073709551616"] {
            let mut values = runtime_env();
            values.insert("AGENT_RUNTIME_FETCH_ENABLED", "true".to_string());
            values.insert("AGENT_FETCH_SOCKET", absolute("fetch.sock"));
            values.insert("AGENT_FETCH_HMAC_KEY_FILE", absolute("fetch.key"));
            values.insert("AGENT_FETCH_POLICY_VERSION", "policy-v1".to_string());
            values.insert(name, invalid.to_string());
            assert!(parse_runtime(&values).unwrap_err().contains(name));
        }
    }

    let mut swap = runtime_env();
    swap.insert(
        "AGENT_RUNTIME_COMMAND_MEMORY_SWAP_MAX_BYTES",
        "0".to_string(),
    );
    assert_eq!(
        parse_runtime(&swap)
            .unwrap()
            .cgroup
            .limits
            .memory_swap_max_bytes,
        0
    );
    for invalid in ["", "-1", "1", "1s", "18446744073709551616"] {
        let mut values = runtime_env();
        values.insert(
            "AGENT_RUNTIME_COMMAND_MEMORY_SWAP_MAX_BYTES",
            invalid.to_string(),
        );
        assert!(
            parse_runtime(&values)
                .unwrap_err()
                .contains("AGENT_RUNTIME_COMMAND_MEMORY_SWAP_MAX_BYTES")
        );
    }

    let mut cpu = runtime_env();
    cpu.insert("AGENT_RUNTIME_COMMAND_CPU_PERIOD_US", "99999".to_string());
    assert!(
        parse_runtime(&cpu)
            .unwrap_err()
            .contains("AGENT_RUNTIME_COMMAND_CPU_QUOTA_US")
    );
}

#[test]
fn runtime_config_rejects_invalid_addresses_paths_and_boolean() {
    for (name, invalid) in [
        ("AGENT_RUNTIME_ADDR", "not-an-address"),
        ("AGENT_RUNTIME_WORKSPACE_ROOT", ""),
        ("AGENT_RUNTIME_SKILLS_ROOT", ""),
        ("AGENT_RUNTIME_TRACE_JSONL", ""),
        ("AGENT_RUNTIME_CGROUP_ROOT", "relative/cgroup"),
        ("AGENT_RUNTIME_CGROUP_AGGREGATE_ROOT", "relative/aggregate"),
    ] {
        let mut values = runtime_env();
        values.insert(name, invalid.to_string());
        let error = parse_runtime(&values).unwrap_err();
        assert!(error.contains(name), "{name}={invalid:?}: {error}");
    }
    for (name, invalid) in [
        ("AGENT_FETCH_SOCKET", "relative/fetch.sock"),
        ("AGENT_FETCH_HMAC_KEY_FILE", "relative/fetch.key"),
        ("AGENT_RUNTIME_REQUIRE_FETCH_FOR_READINESS", "yes"),
    ] {
        let mut values = runtime_env();
        values.insert("AGENT_RUNTIME_FETCH_ENABLED", "true".to_string());
        values.insert("AGENT_FETCH_SOCKET", absolute("fetch.sock"));
        values.insert("AGENT_FETCH_HMAC_KEY_FILE", absolute("fetch.key"));
        values.insert("AGENT_FETCH_POLICY_VERSION", "policy-v1".to_string());
        values.insert(name, invalid.to_string());
        assert!(parse_runtime(&values).unwrap_err().contains(name));
    }
}

#[test]
fn malformed_optional_runtime_values_never_fall_back_to_defaults() {
    for (name, invalid) in [
        ("AGENT_RUNTIME_MAX_OUTPUT_CHARS", "many"),
        ("AGENT_RUNTIME_COMMAND_TIMEOUT_SECS", "2.5"),
        ("AGENT_RUNTIME_FETCH_ENABLED", "TRUE"),
    ] {
        let mut values = runtime_env();
        values.insert(name, invalid.to_string());
        assert!(parse_runtime(&values).unwrap_err().contains(name));
    }
    let mut values = runtime_env();
    values.insert("AGENT_RUNTIME_FETCH_ENABLED", "true".to_string());
    values.insert("AGENT_FETCH_SOCKET", absolute("fetch.sock"));
    values.insert("AGENT_FETCH_HMAC_KEY_FILE", absolute("fetch.key"));
    values.insert("AGENT_FETCH_POLICY_VERSION", "policy-v1".to_string());
    values.insert(
        "AGENT_RUNTIME_REQUIRE_FETCH_FOR_READINESS",
        "TRUE".to_string(),
    );
    assert!(
        parse_runtime(&values)
            .unwrap_err()
            .contains("AGENT_RUNTIME_REQUIRE_FETCH_FOR_READINESS")
    );
}

fn broker_env() -> HashMap<&'static str, String> {
    HashMap::from([
        ("AGENT_FETCH_SOCKET", absolute("broker.sock")),
        ("AGENT_FETCH_PEER_UID", "10001".to_string()),
        ("AGENT_FETCH_PEER_GID", "10001".to_string()),
        ("AGENT_FETCH_HMAC_KEY_FILE", absolute("broker.key")),
        (
            "AGENT_FETCH_DENY_CIDRS",
            "10.0.0.0/8,127.0.0.0/8,::1/128".to_string(),
        ),
        (
            "AGENT_FETCH_DNS_SERVERS",
            "1.1.1.1:53,[2606:4700:4700::1111]:53".to_string(),
        ),
        ("AGENT_FETCH_AUDIT_PATH", absolute("audit.jsonl")),
        ("AGENT_FETCH_POLICY_VERSION", "policy-v1".to_string()),
    ])
}

#[test]
fn broker_config_remains_strict_for_paths_lists_durations_and_limits() {
    for (name, invalid) in [
        ("AGENT_FETCH_SOCKET", "relative.sock"),
        ("AGENT_FETCH_HMAC_KEY_FILE", "relative.key"),
        ("AGENT_FETCH_AUDIT_PATH", "relative.jsonl"),
        ("AGENT_FETCH_DENY_CIDRS", "10.0.0.1"),
        ("AGENT_FETCH_DNS_SERVERS", "1.1.1.1"),
        ("AGENT_FETCH_DNS_TIMEOUT_MS", "1s"),
        ("AGENT_FETCH_CONNECT_TIMEOUT_MS", "0"),
        ("AGENT_FETCH_MAX_CONCURRENCY", "65536"),
        ("AGENT_FETCH_POLICY_VERSION", ""),
    ] {
        let mut values = broker_env();
        values.insert(name, invalid.to_string());
        let error = BrokerConfig::from_env(|key| values.get(key).cloned()).unwrap_err();
        assert!(
            error.to_string().contains(name),
            "{name}={invalid:?}: {error}"
        );
    }
}

#[test]
fn config_debug_output_redacts_runtime_authentication_secret() {
    let config = parse_runtime(&runtime_env()).unwrap();
    let debug = format!("{config:?}");
    assert!(!debug.contains("runtime-secret"));
    assert!(debug.contains("[REDACTED]"));
}
