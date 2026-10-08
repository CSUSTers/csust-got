package session

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

type baselineJSONProbe struct{ calls *atomic.Int64 }

func (p baselineJSONProbe) MarshalJSON() ([]byte, error) {
	p.calls.Add(1)
	return []byte(`{"integer":9007199254740993}`), nil
}

func TestServiceLoadOnlySkipsBaselineSnapshot(t *testing.T) {
	var calls atomic.Int64
	input := &schema.Message{Role: schema.User, Content: "media", Extra: map[string]any{"probe": baselineJSONProbe{calls: &calls}}}
	baseline, err := loadBaseline([]*schema.Message{input}, true)
	require.NoError(t, err)
	require.Nil(t, baseline)
	require.Zero(t, calls.Load(), "load-only must avoid Snapshot's additional JSON passes")
	baseline, err = loadBaseline([]*schema.Message{input}, false)
	require.NoError(t, err)
	require.Positive(t, calls.Load())
	require.Equal(t, json.Number("9007199254740993"), baseline[0].Message.Extra["probe"].(map[string]any)["integer"])
}

func TestServiceDefaultBaselineIsDeeplyDetachedAndKeepsCodecValidation(t *testing.T) {
	capture := mediaCapture(t)
	messages, err := replayRecords(capture.Delta)
	require.NoError(t, err)
	baseline, err := loadBaseline(messages, false)
	require.NoError(t, err)
	want, err := json.Marshal(baseline)
	require.NoError(t, err)
	messages[0].Content = "mutated content"
	*messages[0].UserInputMultiContent[0].Image.Base64Data = "mutated media"
	messages[0].Extra["nested"].([]any)[0] = false
	messages[1].ToolCalls[0].Function.Arguments = "mutated tool arguments"
	messages[3].ResponseMeta.Usage.TotalTokens = 999
	messages[3].AssistantGenMultiContent[0].Reasoning.Signature = "mutated signature"
	got, err := json.Marshal(baseline)
	require.NoError(t, err)
	require.Equal(t, want, got)
	messages[0].Extra["unsupported"] = make(chan int)
	_, err = loadBaseline(messages, false)
	require.Error(t, err, "save-enabled loads must not bypass codec loss/unsupported-value validation")
}

func TestServiceLoadOnlyParentRenewsConfirmsReleasesAndCannotCommit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, repo, scope := newFakeLoadService(t, Options{LeaseDuration: 6 * time.Second, RenewInterval: time.Second})
		loaded, err := svc.Load(t.Context(), Selection{Scope: scope, Mode: SelectLatest, LoadOnly: true})
		require.NoError(t, err)
		require.NotNil(t, loaded.Parent)
		require.Nil(t, loaded.Parent.baseline)
		require.True(t, loaded.Parent.loadOnly)
		active := repo.lastActive
		time.Sleep(12 * time.Second)
		synctest.Wait()
		require.Positive(t, repo.renewed)
		require.Equal(t, active, repo.lastActive, "renewal must not count as user activity")
		require.Equal(t, 1, repo.confirmed)
		id, err := NewID()
		require.NoError(t, err)
		req := CommitRequest{Scope: scope, Agent: "agent", RunID: id, Parent: loaded.Parent,
			Capture: TurnCapture{Delta: History(schema.UserMessage("next"), schema.AssistantMessage("answer", nil)), Complete: true},
			Receipt: DeliveryReceipt{MessageIDs: []int{102}},
		}
		_, err = svc.Commit(t.Context(), req)
		require.ErrorIs(t, err, errSessionLoadOnlyParent, "even a live load-only proof must not accept a Commit")
		require.NoError(t, loaded.Parent.Close())
		_, err = svc.Commit(t.Context(), req)
		require.ErrorIs(t, err, errSessionLoadOnlyParent, "a lost load-only proof must not silently publish an incomplete root")
		require.Equal(t, 1, repo.released)
		require.Empty(t, repo.lease.Token)
	})
}

func TestServiceDefaultLoadKeepsFallbackBaseline(t *testing.T) {
	svc, _, scope := newFakeLoadService(t, Options{})
	loaded, err := svc.LoadWithAcceptance(t.Context(), Selection{Scope: scope, Mode: SelectLatest}, func(_ context.Context, candidate *LoadCandidate) error {
		candidate.Messages[0].Content = "callback mutation"
		return nil
	})
	require.NoError(t, err)
	defer loaded.Parent.Close()
	require.False(t, loaded.Parent.loadOnly)
	require.Len(t, loaded.Parent.baseline, 2)
	require.Equal(t, "root", loaded.Parent.baseline[0].Message.Content)
	loaded.Messages[0].Content = "caller mutation"
	require.Equal(t, "root", loaded.Parent.baseline[0].Message.Content)
}

func BenchmarkServiceLoadBaselineMedia(b *testing.B) {
	var input schema.Message
	if err := json.Unmarshal([]byte(`{"role":"user","user_input_multi_content":[{"type":"image_url","image":{"base64data":"A","mime_type":"image/png"}}]}`), &input); err != nil {
		b.Fatal(err)
	}
	input.UserInputMultiContent[0].Image.Base64Data = new(strings.Repeat("A", 4<<20))
	messages := []*schema.Message{&input, schema.AssistantMessage("answer", nil)}
	for _, loadOnly := range []bool{false, true} {
		name := "save-enabled"
		if loadOnly {
			name = "load-only"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := loadBaseline(messages, loadOnly); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
