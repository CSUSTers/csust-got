package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

func testNode(t *testing.T) Node {
	t.Helper()
	dag, err := NewID()
	require.NoError(t, err)
	id, err := NewID()
	require.NoError(t, err)
	run, err := NewID()
	require.NoError(t, err)
	return Node{Scope: Scope{Namespace: StorageNamespace("test:"), Bot: "../../bot", Platform: "tg/../x", ChatID: -10}, Ref: NodeRef{DAGID: dag, NodeID: id}, Agent: "../../../agent", RunID: run, Version: Version, FileName: id + ".jsonl"}
}

func mediaCapture(t *testing.T) TurnCapture {
	t.Helper()
	var input schema.Message
	require.NoError(t, decodeJSON([]byte(`{"role":"user","content":"<agent_runtime_guidance>user text</agent_runtime_guidance>","multi_content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA","detail":"high"}}],"user_input_multi_content":[{"type":"image_url","image":{"url":"https://example.invalid/image","mime_type":"image/png","detail":"high"}},{"type":"audio_url","audio":{"base64data":"AQID","mime_type":"audio/wav"}},{"type":"video_url","video":{"url":"https://example.invalid/video","mime_type":"video/mp4"}},{"type":"file_url","file":{"base64data":"AQID","mime_type":"application/pdf","name":"doc.pdf"}}],"extra":{"large_integer":9007199254740993,"nested":[true,null,"x"]}}`), &input))
	input.UserInputMultiContent[0].Image.Base64Data = new(strings.Repeat("A", 160000))
	input.UserInputMultiContent = append(input.UserInputMultiContent, schema.MessageInputPart{
		Type: schema.ChatMessagePartType("tool_search_result"),
		ToolSearchResult: &schema.ToolSearchResult{Tools: []*schema.ToolInfo{{
			Name: "search", Desc: "tool definition",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"query": {Type: schema.DataType("string"), Required: true}}),
			Extra:       map[string]any{"provider": "test"},
		}}},
	})
	assistant := &schema.Message{Role: schema.Assistant, ReasoningContent: "tool reasoning", ToolCalls: []schema.ToolCall{{ID: "call1", Type: "function", Function: schema.FunctionCall{Name: "lookup", Arguments: `{"query":"长沙"}`}, Extra: map[string]any{"int": int64(9007199254740993)}}}}
	tool := &schema.Message{Role: schema.Tool, ToolCallID: "call1", ToolName: "lookup", Content: strings.Repeat("长工具结果", 20000)}
	var final schema.Message
	require.NoError(t, decodeJSON([]byte(`{"role":"assistant","content":"answer","reasoning_content":"private reasoning","name":"model","assistant_output_multi_content":[{"type":"reasoning","reasoning":{"text":"thinking","signature":"signed-token"},"extra":{"a":1}},{"type":"image_url","image":{"base64data":"AQID","mime_type":"image/png"}},{"type":"audio_url","audio":{"base64data":"AQID","mime_type":"audio/wav"}},{"type":"video_url","video":{"url":"https://example.invalid/out"}}],"response_meta":{"finish_reason":"stop","usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16},"logprobs":{"content":[{"token":"answer","logprob":-0.125,"bytes":[1,2],"top_logprobs":[]}]}},"extra":{"provider":{"number":9007199254740993}}}`), &final))
	return TurnCapture{Frame: []Record{{Source: SourceFrame, Message: schema.SystemMessage("current system")}}, Bootstrap: History(schema.UserMessage("fallback question"), schema.AssistantMessage("fallback answer", nil)), Delta: []Record{{Source: SourceHistory, Message: &input}, {Source: SourceHistory, Message: assistant}, {Source: SourceGuidance, Message: schema.UserMessage("runtime budget")}, {Source: SourceHistory, Message: tool}, {Source: SourceHistory, Message: &final}}, Complete: true}
}

func TestCodecFullMessagesLongLinesAndSnapshot(t *testing.T) {
	n := testNode(t)
	c := mediaCapture(t)
	snapshot, err := Snapshot(c)
	require.NoError(t, err)
	c.Delta[0].Message.Extra["mutated"] = true
	require.NotContains(t, snapshot.Delta[0].Message.Extra, "mutated")
	var b bytes.Buffer
	require.NoError(t, encodeArchive(&b, n, snapshot))
	h := sha256.Sum256(b.Bytes())
	n.Size, n.Digest = int64(b.Len()), hex.EncodeToString(h[:])
	out, err := decodeArchive(bytes.NewReader(b.Bytes()), n)
	require.NoError(t, err)
	want, err := json.Marshal(snapshot)
	require.NoError(t, err)
	got, err := json.Marshal(out)
	require.NoError(t, err)
	require.Equal(t, string(want), string(got))
	require.Equal(t, json.Number("9007199254740993"), out.Delta[0].Message.Extra["large_integer"])
	replay, err := replayRecords(out.Delta)
	require.NoError(t, err)
	require.Len(t, replay, 4)
	require.Contains(t, replay[0].Content, "<agent_runtime_guidance>")
	require.Equal(t, "signed-token", replay[3].AssistantGenMultiContent[0].Reasoning.Signature)
	require.Equal(t, "call1", replay[2].ToolCallID)
	definition, err := replay[0].UserInputMultiContent[4].ToolSearchResult.Tools[0].ParamsOneOf.ToJSONSchema()
	require.NoError(t, err)
	parameters, err := json.Marshal(definition)
	require.NoError(t, err)
	require.Contains(t, string(parameters), "query")
	require.Equal(t, 16, replay[3].ResponseMeta.Usage.TotalTokens)
	for _, broken := range [][]byte{b.Bytes()[:b.Len()-1], append(append([]byte(nil), b.Bytes()...), '\n'), bytes.Replace(b.Bytes(), []byte("answer"), []byte("broken"), 1)} {
		_, err = decodeArchive(bytes.NewReader(broken), n)
		require.Error(t, err)
	}
	n.Version++
	_, err = decodeArchive(bytes.NewReader(b.Bytes()), n)
	require.ErrorIs(t, err, ErrCorrupt)
	bad := mediaCapture(t)
	bad.Delta[0].Message.Extra["unsupported"] = make(chan int)
	_, err = Snapshot(bad)
	require.Error(t, err)
	bad = mediaCapture(t)
	bad.Delta = bad.Delta[:len(bad.Delta)-2]
	require.ErrorIs(t, ValidateCapture(bad, true), ErrCorrupt)
	bad = TurnCapture{Delta: History(schema.UserMessage("input"), schema.AssistantMessage("", nil)), Complete: true}
	require.ErrorIs(t, ValidateCapture(bad, true), ErrCorrupt)
}

func TestFilesAtomicIsolationAndUnknownPreservation(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileStore(dir)
	require.NoError(t, err)
	defer s.Close()
	n := testNode(t)
	require.NoError(t, s.WithScopeLock(t.Context(), n.Scope, func(f *ScopeFiles) error {
		var err error
		n.Digest, n.Size, err = f.WriteAtomic(n, mediaCapture(t))
		require.NoError(t, err)
		_, err = f.Read(n)
		require.NoError(t, err)
		_, _, err = f.WriteAtomic(n, mediaCapture(t))
		require.Error(t, err)
		malicious := n
		malicious.FileName = "../../outside"
		require.ErrorIs(t, f.Remove(malicious), ErrCorrupt)
		malicious = n
		malicious.Scope.ChatID++
		require.ErrorIs(t, f.Remove(malicious), ErrCorrupt)
		path, err := scopePath(n.Scope)
		require.NoError(t, err)
		unknown := filepath.Join(dir, path, n.Ref.DAGID, "unknown.txt")
		require.NoError(t, os.WriteFile(unknown, []byte("retain"), 0600))
		require.NoError(t, f.Remove(n))
		require.NoError(t, f.Remove(n))
		require.Error(t, f.RemoveEmptyDAG(n.Ref.DAGID))
		got, err := os.ReadFile(unknown)
		require.NoError(t, err)
		require.Equal(t, "retain", string(got))
		return nil
	}))
}

func TestFilesSymlinkEscapeAndLockCancellation(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	s, err := NewFileStore(dir)
	require.NoError(t, err)
	defer s.Close()
	n := testNode(t)
	require.NoError(t, s.WithScopeLock(t.Context(), n.Scope, func(*ScopeFiles) error { return nil }))
	path, err := scopePath(n.Scope)
	require.NoError(t, err)
	link := filepath.Join(dir, path, n.Ref.DAGID)
	if err = os.Symlink(outside, link); err != nil {
		t.Logf("directory symlink unavailable: %v", err)
	} else {
		require.NoError(t, s.WithScopeLock(t.Context(), n.Scope, func(f *ScopeFiles) error {
			_, _, err := f.WriteAtomic(n, mediaCapture(t))
			require.Error(t, err)
			require.Error(t, f.Remove(n))
			return nil
		}))
		entries, err := os.ReadDir(outside)
		require.NoError(t, err)
		require.Empty(t, entries)
	}
	entered, release, complete := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		complete <- s.WithScopeLock(t.Context(), n.Scope, func(*ScopeFiles) error { close(entered); <-release; return nil })
	}()
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	err = s.WithScopeLock(ctx, n.Scope, func(*ScopeFiles) error { t.Error("contended lock entered"); return nil })
	require.ErrorIs(t, err, context.DeadlineExceeded)
	close(release)
	require.NoError(t, <-complete)
}

func TestFilesFailedWriteAndCorruptFinalAreNotPublishedOrBlindlyDeleted(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileStore(dir)
	require.NoError(t, err)
	defer s.Close()
	n := testNode(t)
	require.NoError(t, s.WithScopeLock(t.Context(), n.Scope, func(f *ScopeFiles) error {
		capture := mediaCapture(t)
		capture.Delta[0].Message.Extra["cannot_encode"] = make(chan int)
		_, _, err := f.WriteAtomic(n, capture)
		require.Error(t, err)
		path, err := scopePath(n.Scope)
		require.NoError(t, err)
		final := filepath.Join(dir, path, n.Ref.DAGID, n.FileName)
		_, err = os.Stat(final)
		require.ErrorIs(t, err, os.ErrNotExist)
		require.NoError(t, f.Remove(n))
		require.NoError(t, os.Mkdir(final, 0700))
		_, _, err = f.WriteAtomic(n, mediaCapture(t))
		require.Error(t, err)
		require.ErrorIs(t, f.Remove(n), ErrCorrupt)
		require.NoError(t, os.Remove(final))
		require.NoError(t, f.Remove(n))
		n.Digest, n.Size, err = f.WriteAtomic(n, mediaCapture(t))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(final, []byte("unknown final payload"), 0600))
		require.ErrorIs(t, f.Remove(n), ErrCorrupt)
		data, err := os.ReadFile(final)
		require.NoError(t, err)
		require.Equal(t, "unknown final payload", string(data))
		return nil
	}))
}
