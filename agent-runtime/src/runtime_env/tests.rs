use super::*;
use serde_json::json;

fn env(value: serde_json::Value) -> ApplicationEnv {
    serde_json::from_value(value).unwrap()
}

fn resolve(
    env: &ApplicationEnv,
    layers: &[SkillEnvLayer],
) -> Result<Vec<(String, String)>, EnvError> {
    resolve_environment(
        Some(1),
        env,
        layers,
        &FrozenSkillSnapshot::empty().unwrap(),
        vec![],
    )
}

#[test]
fn static_dotenv_literals_and_rejections() {
    let parsed = parse_dotenv(b" # comment\r\n\r\n API_KEY = ' a ${HOME} $(touch nope) `id` \\n ' \r\nEMPTY=\nMixed_Case=\"literal # not comment\"\nHASH=x#y\n").unwrap();
    assert_eq!(parsed.0["API_KEY"], " a ${HOME} $(touch nope) `id` \\n ");
    assert_eq!(parsed.0["EMPTY"], "");
    assert_eq!(parsed.0["Mixed_Case"], "literal # not comment");
    assert_eq!(parsed.0["HASH"], "x#y");
    for bytes in [
        &b"export API_KEY=secret"[..],
        &b"API_KEY='secret"[..],
        &b"API_KEY=secret\""[..],
        &b"API_KEY=one\nAPI_KEY=secret"[..],
        &b"NO_EQUALS secret"[..],
        &b"API_KEY=\xff"[..],
        &b"API_KEY=secret\0"[..],
        &b"API_KEY='multi\nline'"[..],
        &b"API_KEY=one\rtwo"[..],
    ] {
        let error = parse_dotenv(bytes).unwrap_err();
        assert!(!error.to_string().contains("secret"));
    }
    assert!(!format!("{parsed:?}").contains("touch nope"));
}

#[test]
fn application_names_values_and_every_reserved_family() {
    for name in ["API_KEY", "service_url", "_A", "a1", "Mixed_Case"] {
        validate_application_pair(name, "literal ${HOME}\nvalue").unwrap();
    }
    for name in [
        "",
        "1A",
        "A-B",
        "A=B",
        "BASH_FUNC_x%%",
        "Å",
        "A\0B",
        "PATH",
        "home",
        "SHELL",
        "ENV",
        "BASH_ENV",
        "SHELLOPTS",
        "BASHOPTS",
        "IFS",
        "CDPATH",
        "PWD",
        "OLDPWD",
        "SHLVL",
        "PS4",
        "PROMPT_COMMAND",
        "GLOBIGNORE",
        "TMPDIR",
        "TMP",
        "TEMP",
        "GLIBC_TUNABLES",
        "GCONV_PATH",
        "LOCPATH",
        "NLSPATH",
        "http_proxy",
        "HTTPS_PROXY",
        "ALL_PROXY",
        "NO_PROXY",
        "AGENT_RUNTIME_TOKEN",
        "AGENT_FETCH_CONTROL_FD",
        "LD_PRELOAD",
        "DYLD_INSERT_LIBRARIES",
        "BASH_X",
        "PROOT_X",
        "MALLOC_X",
        "GIT_CONFIG",
        "PYTHONPATH",
        "PERL5OPT",
        "RUBYOPT",
        "NODE_OPTIONS",
        "JAVA_TOOL_OPTIONS",
        "JDK_JAVA_OPTIONS",
        "_JAVA_OPTIONS",
        "SSL_CERT_FILE",
        "CURL_HOME",
        "WGET_X",
    ] {
        assert!(validate_application_pair(name, "secret").is_err(), "{name}");
        assert!(
            validate_application_pair(&name.to_ascii_lowercase(), "secret").is_err(),
            "{name}"
        );
    }
    validate_application_pair(&"A".repeat(128), &"x".repeat(2048)).unwrap();
    assert!(validate_application_pair(&"A".repeat(129), "").is_err());
    assert!(validate_application_pair("A", &"x".repeat(2049)).is_err());
    assert!(validate_application_pair("A", "x\0y").is_err());
}

#[test]
fn ordered_layers_base_override_and_invalid_shadowed_values() {
    let layers: Vec<SkillEnvLayer> = serde_json::from_value(json!([
        {"source":"bot-local", "name":"first", "env":{"A":"first","B":"first"}},
        {"source":"bot-local", "name":"second", "env":{"A":"second","B":"second"}}
    ]))
    .unwrap();
    let result: BTreeMap<_, _> = resolve(&env(json!({"A":"base"})), &layers)
        .unwrap()
        .into_iter()
        .collect();
    assert_eq!(result["A"], "base");
    assert_eq!(result["B"], "second");
    let bad = SkillEnvLayer::BotLocal {
        name: "bad".into(),
        env: env(json!({"PATH":"secret"})),
    };
    assert_eq!(
        resolve(&env(json!({"A":"ok"})), &[bad]).unwrap_err().0,
        "runtime_env_reserved"
    );
    assert!(
        resolve(
            &ApplicationEnv::default(),
            &[layers[0].clone(), layers[0].clone()]
        )
        .is_err()
    );
    assert!(resolve(&ApplicationEnv::default(), &[]).unwrap().is_empty());
}

#[test]
fn versions_shapes_and_duplicate_json_keys_fail_closed() {
    for raw in [r#"{"A":"one","A":"two"}"#, r#"{"A":null}"#, r#"{"A":2}"#] {
        assert!(serde_json::from_str::<ApplicationEnv>(raw).is_err());
    }
    for raw in [
        r#"{"source":"bot-local","name":"s","env":{},"skill_sha256":"x"}"#,
        r#"{"source":"runtime-global","name":"s","skill_sha256":"x","env":{}}"#,
        r#"{"source":"other","name":"s","env":{}}"#,
        r#"{"source":"bot-local","source":"runtime-global","name":"s","env":{}}"#,
        r#"{"source":"bot-local","name":"s","name":"s","env":{}}"#,
        r#"{"source":"bot-local","name":"s"}"#,
    ] {
        assert!(serde_json::from_str::<SkillEnvLayer>(raw).is_err());
    }
    let skills = FrozenSkillSnapshot::empty().unwrap();
    for version in [None, Some(0), Some(2)] {
        assert_eq!(
            resolve_environment(version, &env(json!({"A":"x"})), &[], &skills, vec![])
                .unwrap_err()
                .0,
            "runtime_env_version"
        );
    }
    resolve_environment(None, &ApplicationEnv::default(), &[], &skills, vec![]).unwrap();
    let missing = SkillEnvLayer::RuntimeGlobal {
        name: "unknown".into(),
        skill_sha256: "0".repeat(64),
    };
    assert_eq!(
        resolve(&ApplicationEnv::default(), &[missing])
            .unwrap_err()
            .0,
        "runtime_env_skill"
    );
    let path = SkillEnvLayer::BotLocal {
        name: "../outside".into(),
        env: ApplicationEnv::default(),
    };
    assert_eq!(
        resolve(&ApplicationEnv::default(), &[path]).unwrap_err().0,
        "runtime_env_skill"
    );
}

#[test]
fn budgets_include_overridden_items_escaping_and_internal_environment() {
    let mut map = BTreeMap::new();
    for i in 0..64 {
        map.insert(format!("A{i}"), String::new());
    }
    resolve(&ApplicationEnv(map.clone()), &[]).unwrap();
    map.insert("A64".into(), String::new());
    assert!(resolve(&ApplicationEnv(map), &[]).is_err());
    let layer = |name: &str| SkillEnvLayer::BotLocal {
        name: name.into(),
        env: env(json!({"A":"x".repeat(2048)})),
    };
    assert!(
        resolve(
            &ApplicationEnv::default(),
            &[layer("a"), layer("b"), layer("c"), layer("d")]
        )
        .is_err()
    );
    assert!(resolve(&env(json!({"A":"\u{1}".repeat(1400)})), &[]).is_err());

    let mut pairs: Vec<(String, String)> = (0..4)
        .map(|i| (format!("A{i}"), "x".repeat(2048)))
        .collect();
    let overhead = serde_json::to_vec(&pairs).unwrap().len() - 8192;
    pairs[3].1 = "x".repeat(8192 - overhead - 6144);
    assert!(pairs[3].1.len() <= 2048);
    assert_eq!(serde_json::to_vec(&pairs).unwrap().len(), 8192);
    validate_final_environment(&pairs).unwrap();
    pairs[3].1.push('x');
    assert_eq!(
        validate_final_environment(&pairs).unwrap_err().0,
        "runtime_env_limit"
    );
    pairs[3].1.pop();
    let app = ApplicationEnv(pairs.into_iter().collect());
    assert!(
        resolve_environment(
            Some(1),
            &app,
            &[],
            &FrozenSkillSnapshot::empty().unwrap(),
            vec![("HOME".into(), "/tmp".into())]
        )
        .is_err()
    );

    let layers: Vec<_> = (0..128)
        .map(|i| SkillEnvLayer::BotLocal {
            name: format!("s{i}"),
            env: ApplicationEnv::default(),
        })
        .collect();
    resolve(&ApplicationEnv::default(), &layers).unwrap();
    let mut too_many = layers;
    too_many.push(SkillEnvLayer::BotLocal {
        name: "extra".into(),
        env: ApplicationEnv::default(),
    });
    assert!(resolve(&ApplicationEnv::default(), &too_many).is_err());
    parse_dotenv(format!("#{}", "x".repeat(8191)).as_bytes()).unwrap();
    assert!(parse_dotenv(format!("#{}", "x".repeat(8192)).as_bytes()).is_err());
}

#[test]
fn private_global_snapshot_pins_skill_hash_not_secret_revision() {
    let root = tempfile::tempdir().unwrap();
    std::fs::create_dir(root.path().join("sample")).unwrap();
    std::fs::write(
        root.path().join("sample/SKILL.md"),
        "# Sample\nUseful prose.",
    )
    .unwrap();
    std::fs::write(root.path().join("sample/.env"), "API_KEY=frozen").unwrap();
    let frozen = FrozenSkillSnapshot::load(Some(root.path())).unwrap();
    let hash = frozen.snapshot().skills[0].sha256.clone();
    let layers = [SkillEnvLayer::RuntimeGlobal {
        name: "sample".into(),
        skill_sha256: hash,
    }];
    std::fs::write(root.path().join("sample/.env"), "API_KEY=rotated").unwrap();
    let get = |skills: &FrozenSkillSnapshot| {
        resolve_environment(Some(1), &ApplicationEnv::default(), &layers, skills, vec![]).unwrap()
    };
    assert_eq!(get(&frozen), vec![("API_KEY".into(), "frozen".into())]);
    assert_eq!(
        get(&FrozenSkillSnapshot::load(Some(root.path())).unwrap()),
        vec![("API_KEY".into(), "rotated".into())]
    );
    std::fs::write(
        root.path().join("sample/SKILL.md"),
        "# Sample\nChanged prose.",
    )
    .unwrap();
    let changed = FrozenSkillSnapshot::load(Some(root.path())).unwrap();
    assert!(
        resolve_environment(
            Some(1),
            &ApplicationEnv::default(),
            &layers,
            &changed,
            vec![]
        )
        .is_err()
    );
}
