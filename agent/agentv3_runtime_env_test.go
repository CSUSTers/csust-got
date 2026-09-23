package agentv3

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentV3BashEnvLayersAreTurnLocalAndOrdered(t *testing.T) {
	local := mustAgentV3SkillCatalog(t, agentV3SkillSourceBotLocal, agentV3SkillDescriptor{
		Name: "alpha", Description: "Alpha skill.", Content: alphaSkillContent,
		VirtualPath: "/skills/alpha/SKILL.md", environment: map[string]string{"KEY": "local"},
	})
	global := mustAgentV3SkillCatalog(t, agentV3SkillSourceRuntimeGlobal, agentV3SkillDescriptor{
		Name: "beta", Description: "Beta skill.", Content: "# Beta\nBeta skill.\n", VirtualPath: "/skills/beta/SKILL.md",
	})
	catalog, _, err := mergeAgentV3SkillSnapshots(
		mustSnapshot(t, local.ByName["alpha"]), mustSnapshot(t, global.ByName["beta"]),
	)
	require.NoError(t, err)
	var requests []map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/status" {
			_, _ = w.Write([]byte(`{"ok":true,"runtime_env":true,"bash_env_version":1}`))
			return
		}
		var raw map[string]json.RawMessage
		require.NoError(t, json.NewDecoder(r.Body).Decode(&raw))
		requests = append(requests, raw)
		_, _ = w.Write([]byte(`{"bash_env_version":1,"exit_code":0,"stdout":"ok"}`))
	}))
	defer srv.Close()
	client := &RemoteRuntimeClient{Endpoint: srv.URL, HTTPClient: srv.Client()}
	newTurn := func() *TurnContext {
		return &TurnContext{RuntimeClient: client, V3: &AgentV3TurnState{
			SkillCatalog: catalog, runtimeEnv: map[string]string{"KEY": "base", "LITERAL": "$TOKEN"},
		}}
	}
	first, second := newTurn(), newTurn()
	firstCtx := WithTurnContext(t.Context(), first)
	secondCtx := WithTurnContext(t.Context(), second)
	for _, name := range []string{"beta", "alpha", "beta"} {
		out, err := (&loadSkillTool{}).InvokableRun(firstCtx, `{"name":"`+name+`"}`)
		require.NoError(t, err)
		assert.NotContains(t, out, "$TOKEN")
	}
	_, err = (&remoteBashTool{}).InvokableRun(firstCtx, `{"command":"true"}`)
	require.NoError(t, err)
	_, err = (&remoteBashTool{}).InvokableRun(secondCtx, `{"command":"true"}`)
	require.NoError(t, err)
	require.Len(t, requests, 2)
	assert.JSONEq(t, `[{"source":"runtime-global","name":"beta","skill_sha256":"`+global.ByName["beta"].SHA256+`"},{"source":"bot-local","name":"alpha","env":{"KEY":"local"}}]`, string(requests[0]["skill_env"]))
	assert.JSONEq(t, `{"KEY":"base","LITERAL":"$TOKEN"}`, string(requests[0]["env"]))
	assert.Equal(t, "1", string(requests[0]["bash_env_version"]))
	assert.NotContains(t, requests[1], "skill_env")
	assert.NotContains(t, string(requests[0]["skill_env"]), "virtual_path")
}

func mustSnapshot(t *testing.T, d agentV3SkillDescriptor) agentV3SkillSnapshot {
	t.Helper()
	snapshot, err := newAgentV3SkillSnapshot(d.Source, []agentV3SkillDescriptor{d})
	require.NoError(t, err)
	return snapshot
}

func TestAgentV3BashEnvCapabilityAndErrorRedaction(t *testing.T) {
	const secret = "synthetic-secret-value"
	for _, tc := range []struct {
		name       string
		status     string
		bashStatus int
		bashBody   string
		wantBash   bool
		wantError  string
	}{
		{"old status", `{"ok":true}`, 200, `{"stdout":"ignored"}`, false, "does not support"},
		{"wrong status version", `{"ok":true,"runtime_env":true,"bash_env_version":2}`, 200, `{"stdout":"ignored"}`, false, "does not support"},
		{"missing response version", `{"ok":true,"runtime_env":true,"bash_env_version":1}`, 200, `{"stdout":"ok"}`, true, "does not support"},
		{"bad status body", `{"ok":true,"runtime_env":true,"bash_env_version":1}`, 400, secret, true, "request failed"},
		{"runtime error", `{"ok":true,"runtime_env":true,"bash_env_version":1}`, 200, `{"bash_env_version":1,"error":"` + secret + `"}`, true, "request failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bashCalls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/status" {
					_, _ = w.Write([]byte(tc.status))
					return
				}
				bashCalls++
				w.WriteHeader(tc.bashStatus)
				_, _ = w.Write([]byte(tc.bashBody))
			}))
			defer srv.Close()
			client := &RemoteRuntimeClient{Endpoint: srv.URL, HTTPClient: srv.Client()}
			_, err := client.Bash(t.Context(), runtimeBashRequest{BashEnvVersion: 1, Env: map[string]string{"SECRET": secret}, Command: "true"})
			require.ErrorContains(t, err, tc.wantError)
			assert.NotContains(t, err.Error(), secret)
			assert.Equal(t, tc.wantBash, bashCalls > 0)
		})
	}
}

func TestAgentV3BashEnvRejectsCombinedOverBudgetBeforeHTTP(t *testing.T) {
	client := &RemoteRuntimeClient{Endpoint: "http://runtime.invalid", HTTPClient: &http.Client{Transport: testRoundTripper(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid env contacted runtime")
		return nil, nil
	})}}
	first := make(map[string]string)
	for i := range 64 {
		first["ITEM_"+strconv.Itoa(i)] = "x"
	}
	_, err := client.Bash(t.Context(), runtimeBashRequest{
		BashEnvVersion: 1, Env: map[string]string{"ITEM_0": "override"},
		SkillEnv: []runtimeSkillEnvLayer{{Source: agentV3SkillSourceBotLocal, Name: "alpha", Env: first}},
	})
	require.ErrorIs(t, err, errRuntimeEnvResponse)
}

func TestAgentV3BashEnvRejectsOversizedResponseWithValidPrefix(t *testing.T) {
	const secret = "synthetic-secret-tail"
	prefix := `{"bash_env_version":1,"stdout":"` + strings.Repeat("x", 2*1024*1024-len(`{"bash_env_version":1,"stdout":"`)-len(`"}`)) + `"}`
	require.Len(t, prefix, 2*1024*1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/status" {
			_, _ = w.Write([]byte(`{"ok":true,"runtime_env":true,"bash_env_version":1}`))
			return
		}
		_, _ = w.Write([]byte(prefix + secret))
	}))
	defer srv.Close()
	client := &RemoteRuntimeClient{Endpoint: srv.URL, HTTPClient: srv.Client()}
	_, err := client.Bash(t.Context(), runtimeBashRequest{BashEnvVersion: 1, Env: map[string]string{"SECRET": "value"}, Command: "true"})
	require.ErrorIs(t, err, errRuntimeEnvResponse)
	assert.NotContains(t, err.Error(), secret)
}

func TestAgentV3BashEnvDoesNotFollowRedirects(t *testing.T) {
	for _, redirected := range []string{"status", "bash"} {
		t.Run(redirected, func(t *testing.T) {
			targetCalls := 0
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls++ }))
			defer target.Close()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.TrimPrefix(r.URL.Path, "/v1/") == redirected {
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
					return
				}
				_, _ = w.Write([]byte(`{"ok":true,"runtime_env":true,"bash_env_version":1}`))
			}))
			defer srv.Close()
			client := &RemoteRuntimeClient{Endpoint: srv.URL, HTTPClient: srv.Client(), AuthToken: "token"}
			_, err := client.Bash(t.Context(), runtimeBashRequest{BashEnvVersion: 1, Env: map[string]string{"SECRET": "value"}, Command: "true"})
			require.Error(t, err)
			assert.Zero(t, targetCalls)
		})
	}
}

func TestAgentV3BotLocalSkillWithoutDotenvSendsEmptyEnv(t *testing.T) {
	tc := &TurnContext{V3: &AgentV3TurnState{}}
	tc.activateSkill(agentV3SkillDescriptor{Name: "alpha", Source: agentV3SkillSourceBotLocal})
	_, layers := tc.runtimeEnvironment()
	require.Len(t, layers, 1)
	data, err := json.Marshal(layers[0])
	require.NoError(t, err)
	assert.JSONEq(t, `{"source":"bot-local","name":"alpha","env":{}}`, string(data))
}

func TestAgentV3SkillEnvironmentUsesOnlyCatalogWinner(t *testing.T) {
	local := mustSnapshot(t, agentV3SkillDescriptor{
		Name: "same", Source: agentV3SkillSourceBotLocal, Description: "Local skill.",
		Content: "# Local\nLocal skill.\n", VirtualPath: "/skills/same/SKILL.md",
		environment: map[string]string{"PRIVATE": "synthetic-hidden-value"},
	})
	builtin := mustSnapshot(t, agentV3SkillDescriptor{
		Name: "same", Source: agentV3SkillSourceBuiltin, Description: "Built-in skill.",
		Content: "# Built-in\nBuilt-in skill.\n",
	})
	catalog, _, err := mergeAgentV3SkillSnapshots(local, builtin)
	require.NoError(t, err)
	tc := &TurnContext{V3: &AgentV3TurnState{SkillCatalog: catalog}}
	ctx := WithTurnContext(t.Context(), tc)
	_, layers := tc.runtimeEnvironment()
	assert.Empty(t, layers)
	missing, err := (&loadSkillTool{}).InvokableRun(ctx, `{"name":"missing"}`)
	require.NoError(t, err)
	assert.Contains(t, missing, "Skill Error")
	out, err := (&loadSkillTool{}).InvokableRun(ctx, `{"name":"same"}`)
	require.NoError(t, err)
	assert.Contains(t, out, `source="builtin"`)
	assert.NotContains(t, out, "synthetic-hidden-value")
	_, layers = tc.runtimeEnvironment()
	assert.Empty(t, layers)
}
