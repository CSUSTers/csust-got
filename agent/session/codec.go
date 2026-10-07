package session

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/cloudwego/eino/schema"
)

const maxArchiveBytes int64 = 256 << 20
const maxArchiveRecords = 100000

var errArchiveLimit = errors.New("session archive exceeds size limit")

type header struct {
	Kind           string   `json:"kind"`
	Version        int      `json:"version"`
	Scope          Scope    `json:"scope"`
	Ref            NodeRef  `json:"ref"`
	Parent         *NodeRef `json:"parent,omitempty"`
	Agent          string   `json:"agent"`
	RunID          string   `json:"run_id"`
	Complete       bool     `json:"complete"`
	FrameCount     int      `json:"frame_count"`
	BootstrapCount int      `json:"bootstrap_count"`
	DeltaCount     int      `json:"delta_count"`
}

type trailer struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

// Snapshot detaches all JSON values and rejects messages that cannot round-trip intact.
func Snapshot(c TurnCapture) (TurnCapture, error) {
	if len(c.Frame)+len(c.Bootstrap)+len(c.Delta) > maxArchiveRecords {
		return TurnCapture{}, fmt.Errorf("%w: too many archive records", ErrCorrupt)
	}
	b, err := json.Marshal(c)
	if err != nil {
		return TurnCapture{}, err
	}
	if int64(len(b)) > maxArchiveBytes {
		return TurnCapture{}, fmt.Errorf("%w: maximum %d bytes", errArchiveLimit, maxArchiveBytes)
	}
	var out TurnCapture
	if err = decodeJSON(b, &out); err != nil {
		return TurnCapture{}, err
	}
	// Reject custom marshalers whose JSON cannot be represented by schema.Message.
	canonical, err := json.Marshal(out)
	if err != nil {
		return TurnCapture{}, err
	}
	if bytes.Equal(b, canonical) {
		return out, nil
	}
	var original, restored any
	if err = decodeJSON(b, &original); err != nil {
		return TurnCapture{}, err
	}
	if err = decodeJSON(canonical, &restored); err != nil {
		return TurnCapture{}, err
	}
	a, _ := json.Marshal(original)
	z, _ := json.Marshal(restored)
	if !bytes.Equal(a, z) {
		return TurnCapture{}, fmt.Errorf("%w: non-round-trippable schema message", ErrCorrupt)
	}
	return out, nil
}

func decodeJSON(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return ErrCorrupt
	}
	return nil
}

func replayRecords(records []Record) ([]*schema.Message, error) {
	var out []*schema.Message
	for _, r := range records {
		if r.Message == nil {
			return nil, ErrCorrupt
		}
		switch r.Source {
		case SourceHistory:
			if r.Message.Role == schema.System {
				return nil, fmt.Errorf("%w: system must be frame", ErrCorrupt)
			}
			out = append(out, r.Message)
		case SourceFrame, SourceGuidance:
		default:
			return nil, ErrCorrupt
		}
	}
	return out, nil
}

// ValidateHistory rejects unknown roles and incomplete or mismatched tool chains.
func ValidateHistory(messages []*schema.Message) error {
	pending := map[string]string{}
	for _, m := range messages {
		if m == nil {
			return ErrCorrupt
		}
		if len(pending) > 0 && m.Role != schema.Tool {
			return fmt.Errorf("%w: missing tool responses", ErrCorrupt)
		}
		switch m.Role {
		case schema.User, schema.Assistant:
		case schema.Tool:
			name, ok := pending[m.ToolCallID]
			if !ok || m.ToolName != "" && m.ToolName != name {
				return fmt.Errorf("%w: unmatched tool response", ErrCorrupt)
			}
			delete(pending, m.ToolCallID)
		default:
			return ErrCorrupt
		}
		if len(m.ToolCalls) > 0 && m.Role != schema.Assistant {
			return ErrCorrupt
		}
		for _, call := range m.ToolCalls {
			if call.ID == "" || call.Function.Name == "" {
				return ErrCorrupt
			}
			if _, ok := pending[call.ID]; ok {
				return ErrCorrupt
			}
			pending[call.ID] = call.Function.Name
		}
	}
	if len(pending) != 0 {
		return fmt.Errorf("%w: dangling tool call", ErrCorrupt)
	}
	return nil
}

// ValidateCapture requires a complete turn and permits bootstrap only on roots.
func ValidateCapture(c TurnCapture, root bool) error {
	if !c.Complete || !root && len(c.Bootstrap) != 0 {
		return ErrCorrupt
	}
	for _, r := range c.Frame {
		if r.Message == nil || r.Source != SourceFrame && r.Source != SourceGuidance {
			return ErrCorrupt
		}
	}
	b, err := replayRecords(c.Bootstrap)
	if err != nil {
		return err
	}
	if err = ValidateHistory(b); err != nil {
		return err
	}
	d, err := replayRecords(c.Delta)
	if err != nil {
		return err
	}
	if len(d) < 2 || d[0].Role != schema.User || d[len(d)-1].Role != schema.Assistant || len(d[len(d)-1].ToolCalls) != 0 {
		return ErrCorrupt
	}
	final := d[len(d)-1]
	if final.Content == "" && final.ReasoningContent == "" && len(final.MultiContent) == 0 && len(final.AssistantGenMultiContent) == 0 {
		return fmt.Errorf("%w: empty final assistant", ErrCorrupt)
	}
	return ValidateHistory(d)
}

func encodeArchive(w io.Writer, n Node, c TurnCapture) error {
	if err := ValidateCapture(c, n.Parent == nil); err != nil {
		return err
	}
	h := header{Kind: "header", Version: Version, Scope: n.Scope, Ref: n.Ref, Parent: n.Parent, Agent: n.Agent, RunID: n.RunID, Complete: c.Complete, FrameCount: len(c.Frame), BootstrapCount: len(c.Bootstrap), DeltaCount: len(c.Delta)}
	e := json.NewEncoder(w)
	if err := e.Encode(h); err != nil {
		return err
	}
	for _, records := range [][]Record{c.Frame, c.Bootstrap, c.Delta} {
		for _, r := range records {
			if err := e.Encode(r); err != nil {
				return err
			}
		}
	}
	return e.Encode(trailer{Kind: "complete", Count: h.FrameCount + h.BootstrapCount + h.DeltaCount})
}

func decodeArchive(r io.Reader, n Node) (TurnCapture, error) {
	if n.Version != Version || n.Size <= 0 || n.Size > maxArchiveBytes {
		return TurnCapture{}, ErrCorrupt
	}
	hash := sha256.New()
	reader := bufio.NewReader(io.TeeReader(io.LimitReader(r, n.Size+1), hash))
	read := func(v any) error {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return fmt.Errorf("%w: truncated JSONL", ErrCorrupt)
		}
		return decodeJSON(line, v)
	}
	var h header
	if err := read(&h); err != nil {
		return TurnCapture{}, err
	}
	want, _ := json.Marshal(n.Parent)
	got, _ := json.Marshal(h.Parent)
	if h.Kind != "header" || h.Version != Version || h.Scope != n.Scope || h.Ref != n.Ref || !bytes.Equal(want, got) || h.Agent != n.Agent || h.RunID != n.RunID || !h.Complete {
		return TurnCapture{}, ErrCorrupt
	}
	counts := []int{h.FrameCount, h.BootstrapCount, h.DeltaCount}
	total := int64(0)
	for _, count := range counts {
		if count < 0 {
			return TurnCapture{}, ErrCorrupt
		}
		total += int64(count)
	}
	if total < 0 || total > maxArchiveRecords || total > n.Size/2 {
		return TurnCapture{}, ErrCorrupt
	}
	groups := make([][]Record, 3)
	for i, count := range counts {
		for range count {
			var record Record
			if err := read(&record); err != nil {
				return TurnCapture{}, err
			}
			if record.Message == nil || record.Source != SourceHistory && record.Source != SourceFrame && record.Source != SourceGuidance {
				return TurnCapture{}, ErrCorrupt
			}
			groups[i] = append(groups[i], record)
		}
	}
	var end trailer
	if err := read(&end); err != nil {
		return TurnCapture{}, err
	}
	if end.Kind != "complete" || int64(end.Count) != total {
		return TurnCapture{}, ErrCorrupt
	}
	if _, err := reader.ReadByte(); err != io.EOF {
		return TurnCapture{}, ErrCorrupt
	}
	if hex.EncodeToString(hash.Sum(nil)) != n.Digest {
		return TurnCapture{}, fmt.Errorf("%w: digest", ErrCorrupt)
	}
	c := TurnCapture{Frame: groups[0], Bootstrap: groups[1], Delta: groups[2], Complete: true}
	return c, ValidateCapture(c, n.Parent == nil)
}
