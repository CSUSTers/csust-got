package agentv3

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errOriginalCheckRedirect = errors.New("original redirect handler called")

func TestRemoteRuntimeSkillsSnapshotAuthenticatesAndValidates(t *testing.T) {
	snapshot := mustAgentV3RuntimeSnapshot(t)
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1/skills", r.URL.Path)
		assert.Equal(t, "Bearer runtime-secret", r.Header.Get("Authorization"))
		require.NoError(t, json.NewEncoder(w).Encode(snapshot))
	}))
	defer server.Close()

	client := &RemoteRuntimeClient{Endpoint: server.URL, AuthToken: "runtime-secret", HTTPClient: server.Client()}
	got, err := client.SkillsSnapshot(t.Context())

	require.NoError(t, err)
	assert.Equal(t, snapshot, got)
	assert.Equal(t, int32(1), requestCount.Load())
}

func TestRemoteRuntimeSkillsSnapshotRejectsRedirects(t *testing.T) {
	statuses := []int{
		http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect,
	}

	for _, status := range statuses {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var sourceRequestCount atomic.Int32
			var targetRequestCount atomic.Int32
			var originalRedirectCalls atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				targetRequestCount.Add(1)
			}))
			defer target.Close()
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				sourceRequestCount.Add(1)
				w.Header().Set("Location", target.URL+"/redirect-location-secret")
				w.WriteHeader(status)
				_, _ = w.Write([]byte("redirect-body-secret"))
			}))
			defer source.Close()

			httpClient := source.Client()
			httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
				originalRedirectCalls.Add(1)
				return errOriginalCheckRedirect
			}
			client := &RemoteRuntimeClient{
				Endpoint:   source.URL,
				AuthToken:  "runtime-auth-secret",
				HTTPClient: httpClient,
			}

			_, err := client.SkillsSnapshot(t.Context())

			require.ErrorIs(t, err, errRuntimeHTTPStatus)
			assert.Equal(t, fmt.Sprintf("agent v3 runtime returned non-success status: runtime /v1/skills returned %d", status), err.Error())
			assert.Equal(t, int32(1), sourceRequestCount.Load())
			assert.Equal(t, int32(0), targetRequestCount.Load())
			assert.Equal(t, int32(0), originalRedirectCalls.Load())
			assert.NotContains(t, err.Error(), "redirect-location-secret")
			assert.NotContains(t, err.Error(), "redirect-body-secret")
			assert.NotContains(t, err.Error(), "runtime-auth-secret")

			require.NotNil(t, httpClient.CheckRedirect)
			assert.ErrorIs(t, httpClient.CheckRedirect(&http.Request{}, nil), errOriginalCheckRedirect)
			assert.Equal(t, int32(1), originalRedirectCalls.Load())
		})
	}
}

func TestRemoteRuntimeSkillsSnapshotRejectsInvalidResponses(t *testing.T) {
	valid := mustAgentV3RuntimeSnapshot(t)
	validBody := mustJSON(t, valid)
	tooLarge := append([]byte(`{"schema_version":1,"snapshot_sha256":"`), make([]byte, agentV3RuntimeSkillsResponseMaxBytes+1)...)
	invalidUTF8 := append([]byte(nil), validBody...)
	for i := range invalidUTF8 {
		if invalidUTF8[i] == 'r' {
			invalidUTF8[i] = 0xff
			break
		}
	}

	unsorted := valid
	unsorted.Skills = append(unsorted.Skills, unsorted.Skills[0])
	unsorted.Skills[0].Name, unsorted.Skills[1].Name = "zeta", "alpha"
	unsorted.Skills[0].VirtualPath = agentV3SkillVirtualPath(agentV3SkillSourceRuntimeGlobal, "zeta")
	unsorted.Skills[1].VirtualPath = agentV3SkillVirtualPath(agentV3SkillSourceRuntimeGlobal, "alpha")
	unsorted.Skills[0].SHA256 = agentV3SkillContentSHA256(unsorted.Skills[0].Content)
	unsorted.Skills[1].SHA256 = agentV3SkillContentSHA256(unsorted.Skills[1].Content)
	unsorted.SnapshotSHA256 = agentV3SkillSnapshotSHA256(unsorted.SchemaVersion, unsorted.Skills)

	invalidName := valid
	invalidName.Skills[0].Name = "Invalid Name"
	invalidName.Skills[0].VirtualPath = agentV3SkillVirtualPath(agentV3SkillSourceRuntimeGlobal, invalidName.Skills[0].Name)
	invalidName.SnapshotSHA256 = agentV3SkillSnapshotSHA256(invalidName.SchemaVersion, invalidName.Skills)

	invalidSource := valid
	invalidSource.Skills[0].Source = agentV3SkillSourceBotLocal
	invalidSource.SnapshotSHA256 = agentV3SkillSnapshotSHA256(invalidSource.SchemaVersion, invalidSource.Skills)

	invalidPath := valid
	invalidPath.Skills[0].VirtualPath = "/not-a-skill"
	invalidPath.SnapshotSHA256 = agentV3SkillSnapshotSHA256(invalidPath.SchemaVersion, invalidPath.Skills)

	invalidLimits := valid
	invalidLimits.Skills[0].Content = string(make([]byte, agentV3SkillFileMaxBytes+1))
	invalidLimits.Skills[0].SHA256 = agentV3SkillContentSHA256(invalidLimits.Skills[0].Content)
	invalidLimits.SnapshotSHA256 = agentV3SkillSnapshotSHA256(invalidLimits.SchemaVersion, invalidLimits.Skills)

	duplicate := valid
	duplicate.Skills = append(duplicate.Skills, duplicate.Skills[0])
	duplicate.SnapshotSHA256 = agentV3SkillSnapshotSHA256(duplicate.SchemaVersion, duplicate.Skills)

	badContentHash := valid
	badContentHash.Skills[0].SHA256 = "bad"
	badContentHash.SnapshotSHA256 = agentV3SkillSnapshotSHA256(badContentHash.SchemaVersion, badContentHash.Skills)

	badSnapshotHash := valid
	badSnapshotHash.SnapshotSHA256 = "bad"

	tests := []struct {
		name       string
		statusCode int
		body       []byte
	}{
		{name: "non-2xx", statusCode: http.StatusUnauthorized, body: []byte("runtime-secret")},
		{name: "larger than eight mib", statusCode: http.StatusOK, body: tooLarge},
		{name: "empty", statusCode: http.StatusOK},
		{name: "truncated json", statusCode: http.StatusOK, body: validBody[:len(validBody)-1]},
		{name: "trailing json", statusCode: http.StatusOK, body: append(append([]byte(nil), validBody...), validBody...)},
		{name: "unknown field", statusCode: http.StatusOK, body: []byte(`{"schema_version":1,"snapshot_sha256":"x","skills":[],"unexpected":true}`)},
		{name: "wrong schema", statusCode: http.StatusOK, body: []byte(`{"schema_version":2,"snapshot_sha256":"x","skills":[]}`)},
		{name: "unsorted", statusCode: http.StatusOK, body: mustJSON(t, unsorted)},
		{name: "invalid canonical name", statusCode: http.StatusOK, body: mustJSON(t, invalidName)},
		{name: "invalid source", statusCode: http.StatusOK, body: mustJSON(t, invalidSource)},
		{name: "invalid virtual path", statusCode: http.StatusOK, body: mustJSON(t, invalidPath)},
		{name: "invalid utf-8", statusCode: http.StatusOK, body: invalidUTF8},
		{name: "invalid limits", statusCode: http.StatusOK, body: mustJSON(t, invalidLimits)},
		{name: "duplicate", statusCode: http.StatusOK, body: mustJSON(t, duplicate)},
		{name: "content hash mismatch", statusCode: http.StatusOK, body: mustJSON(t, badContentHash)},
		{name: "snapshot hash mismatch", statusCode: http.StatusOK, body: mustJSON(t, badSnapshotHash)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write(tt.body)
			}))
			defer server.Close()

			client := &RemoteRuntimeClient{Endpoint: server.URL, AuthToken: "runtime-secret", HTTPClient: server.Client()}
			_, err := client.SkillsSnapshot(t.Context())

			require.Error(t, err)
			assert.NotContains(t, err.Error(), "runtime-secret")
		})
	}
}

func mustAgentV3RuntimeSnapshot(t *testing.T) agentV3SkillSnapshot {
	t.Helper()
	snapshot, err := newAgentV3SkillSnapshot(agentV3SkillSourceRuntimeGlobal, []agentV3SkillDescriptor{{
		Name:        "runtime-skill",
		Description: "Runtime skill description.",
		Content:     "# Runtime skill\nRuntime skill description.\n",
		VirtualPath: "/skills/runtime-skill/SKILL.md",
	}})
	require.NoError(t, err)
	return snapshot
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	require.NoError(t, err)
	return body
}
