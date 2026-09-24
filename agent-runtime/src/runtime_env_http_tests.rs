#[tokio::test]
async fn env_wire_rejections_are_safe_and_precede_launch() {
    let mut state = test_state();
    state.command_supervisor = None;
    let workspace = state.workspace_root.clone();
    let router = app(state);
    for (extra, category) in [
        (json!({"env":{"API_KEY":"synthetic-secret"}}), "runtime_env_version"),
        (json!({"bash_env_version":2}), "runtime_env_version"),
        (json!({"bash_env_version":1,"env":{"PATH":"synthetic-secret"}}), "runtime_env_reserved"),
        (json!({"bash_env_version":1,"env":{"API_KEY":false}}), "runtime_env_request"),
        (json!({"bash_env_version":1,"skill_env":[{"source":"runtime-global","name":"missing","skill_sha256":"wrong"}]}), "runtime_env_skill"),
        (json!({"bash_env_version":1,"skill_env":[{"source":"runtime-global","name":"missing","skill_sha256":"wrong","env":{"API_KEY":"synthetic-secret"}}]}), "runtime_env_request"),
        (json!({"bash_env_version":1,"skill_env":[{"source":"bot-local","name":"/absolute/path","env":{}}]}), "runtime_env_skill"),
    ] {
        let mut request = json!({"namespace":"env-rejected","run_id":"run","command":"echo must-not-run"});
        request.as_object_mut().unwrap().extend(extra.as_object().unwrap().clone());
        let response = router.clone().oneshot(request_with_json("/v1/bash", request)).await.unwrap();
        assert_eq!(response.status(), StatusCode::BAD_REQUEST);
        let body = to_bytes(response.into_body(), usize::MAX).await.unwrap();
        assert_eq!(serde_json::from_slice::<serde_json::Value>(&body).unwrap(), json!({"error":category}));
        assert!(!String::from_utf8_lossy(&body).contains("synthetic-secret"));
    }
    let response = router.clone().oneshot(Request::builder().method("POST").uri("/v1/bash")
        .header("content-type", "application/json")
        .body(Body::from(r#"{"namespace":"env-rejected","run_id":"run","command":"echo no","bash_env_version":1,"env":{"API_KEY":"one","API_KEY":"synthetic-secret"}}"#)).unwrap()).await.unwrap();
    assert_eq!(response.status(), StatusCode::BAD_REQUEST);
    assert_eq!(to_bytes(response.into_body(), usize::MAX).await.unwrap(), r#"{"error":"runtime_env_request"}"#);
    for layer in [
        r#"{"source":"bot-local","source":"runtime-global","name":"s","env":{}}"#,
        r#"{"source":"bot-local","name":"s","name":"other","env":{}}"#,
    ] {
        let body = format!(
            r#"{{"namespace":"env-rejected","run_id":"run","command":"echo no","bash_env_version":1,"skill_env":[{layer}]}}"#
        );
        let response = router.clone().oneshot(Request::builder().method("POST").uri("/v1/bash")
            .header("content-type", "application/json")
            .body(Body::from(body)).unwrap()).await.unwrap();
        assert_eq!(response.status(), StatusCode::BAD_REQUEST);
        assert_eq!(to_bytes(response.into_body(), usize::MAX).await.unwrap(), r#"{"error":"runtime_env_request"}"#);
    }
    assert!(!workspace.join(crate::identity::namespace_storage_key("env-rejected")).exists());
}

#[tokio::test]
async fn env_http_capability_real_child_precedence_and_request_isolation() {
    let mut state = test_state();
    state.max_output_chars = 16 * 1024;
    let skills = tempfile::tempdir().unwrap();
    std::fs::create_dir(skills.path().join("global")).unwrap();
    std::fs::write(skills.path().join("global/SKILL.md"), "# Global\nExample skill.").unwrap();
    std::fs::write(skills.path().join("global/.env"), "GLOBAL_MARKER=global-synthetic\nPRIORITY=global\n").unwrap();
    state.skill_snapshot = skills::FrozenSkillSnapshot::load(Some(skills.path())).unwrap();
    let hash = state.skill_snapshot.snapshot().skills[0].sha256.clone();
    let router = app(state);
    let status = router.clone().oneshot(get_request("/v1/status")).await.unwrap();
    let status: serde_json::Value = serde_json::from_slice(&to_bytes(status.into_body(), usize::MAX).await.unwrap()).unwrap();
    assert_eq!(status["runtime_env"], true);
    assert_eq!(status["bash_env_version"], 1);
    let public = router.clone().oneshot(get_request("/v1/skills")).await.unwrap();
    let public = to_bytes(public.into_body(), usize::MAX).await.unwrap();
    assert!(!String::from_utf8_lossy(&public).contains("global-synthetic"));
    assert!(!String::from_utf8_lossy(&public).contains("GLOBAL_MARKER"));
    let command = if cfg!(windows) { "set" } else { "env" };
    let response = router.clone().oneshot(request_with_json("/v1/bash", json!({
        "namespace":"env-real", "run_id":"run-one", "command":command,
        "bash_env_version":1,
        "env":{"PRIORITY":"base", "LITERAL_MARKER":"${HOME} $(echo not-executed) `id` \\n #literal"},
        "skill_env":[
            {"source":"bot-local", "name":"local", "env":{"PRIORITY":"local", "LOCAL_MARKER":"local-synthetic"}},
            {"source":"runtime-global", "name":"global", "skill_sha256":hash}
        ]
    }))).await.unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let body: BashResponse = serde_json::from_slice(&to_bytes(response.into_body(), usize::MAX).await.unwrap()).unwrap();
    assert_eq!(body.exit_code, 0, "{}", body.stderr);
    assert_eq!(body.bash_env_version, 1);
    let values: std::collections::BTreeMap<_,_> = body.stdout.lines().filter_map(|l| l.split_once('=')).collect();
    assert_eq!(values["PRIORITY"], "base");
    assert_eq!(values["GLOBAL_MARKER"], "global-synthetic");
    assert_eq!(values["LOCAL_MARKER"], "local-synthetic");
    assert_eq!(values["LITERAL_MARKER"], "${HOME} $(echo not-executed) `id` \\n #literal");
    assert_eq!(values["AGENT_FETCH_CONTROL_FD"], "4");
    for extras in [json!({}), json!({"env":{},"skill_env":[]}), json!({"env":null,"skill_env":null})] {
        let mut request = json!({"namespace":"env-real","run_id":"run-next","command":command});
        request.as_object_mut().unwrap().extend(extras.as_object().unwrap().clone());
        let response = router.clone().oneshot(request_with_json("/v1/bash", request)).await.unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        let body: BashResponse = serde_json::from_slice(&to_bytes(response.into_body(), usize::MAX).await.unwrap()).unwrap();
        for absent in ["PRIORITY=", "GLOBAL_MARKER=", "LOCAL_MARKER=", "LITERAL_MARKER="] {
            assert!(!body.stdout.contains(absent), "environment persisted across requests");
        }
    }
}

#[tokio::test(flavor = "multi_thread")]
async fn env_http_full_spec_budget_rejects_without_target_or_jail_leak() {
    let state = test_state();
    let workspace = test_workspace(&state, "env-budget");
    let router = app(state);
    let response = router.oneshot(request_with_json("/v1/bash", json!({
        "namespace":"env-budget","run_id":"run","command":format!("echo no > must-not-exist; {}", "x".repeat(32768)),
        "bash_env_version":1,"env":{"API_KEY":"synthetic-small"}
    }))).await.unwrap();
    assert_eq!(response.status(), StatusCode::BAD_REQUEST);
    let body = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    assert_eq!(serde_json::from_slice::<serde_json::Value>(&body).unwrap(), json!({"error":"runtime_env_exec_spec_limit"}));
    assert!(!workspace.join("must-not-exist").exists());
}
