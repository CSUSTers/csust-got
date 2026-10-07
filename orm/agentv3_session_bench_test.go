package orm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"

	"csust-got/agent/session"

	"github.com/cloudwego/eino/schema"
	"github.com/redis/go-redis/v9"
)

type sessionCommandStats struct {
	commands                map[string]int64
	readValues, writeValues map[string]int64
	logical, wire, wireTime int64
	getBytes, setBytes      int64
	pipelines, conflicts    int64
}

type sessionCommandCounter struct {
	mu              sync.Mutex
	stats           sessionCommandStats
	timeIntercepted bool
	trace           bool
	events          []sessionCommandEvent
}

type sessionCommandEvent struct {
	name, key, field string
	written          int64
}

func newSessionStats() sessionCommandStats {
	return sessionCommandStats{commands: map[string]int64{}, readValues: map[string]int64{}, writeValues: map[string]int64{}}
}
func (c *sessionCommandCounter) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats = newSessionStats()
	c.events = nil
}
func (c *sessionCommandCounter) snapshot() sessionCommandStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.stats
	s.commands = map[string]int64{}
	s.readValues = map[string]int64{}
	s.writeValues = map[string]int64{}
	for k, n := range c.stats.commands {
		s.commands[k] = n
	}
	for k, n := range c.stats.readValues {
		s.readValues[k] = n
	}
	for k, n := range c.stats.writeValues {
		s.writeValues[k] = n
	}
	return s
}

func sessionPayloadLen(v any) int64 {
	if b, ok := v.([]byte); ok {
		return int64(len(b))
	}
	return int64(len(fmt.Sprint(v)))
}

func (c *sessionCommandCounter) record(cmd redis.Cmder) {
	c.mu.Lock()
	defer c.mu.Unlock()
	name := cmd.Name()
	c.stats.commands[name]++
	c.stats.logical++
	if name != "time" || !c.timeIntercepted {
		c.stats.wire++
		if name == "time" {
			c.stats.wireTime++
		}
	}
	args := cmd.Args()
	if len(args) < 2 {
		return
	}
	key := fmt.Sprint(args[1])
	if !strings.Contains(key, "agentv3:session:") {
		return
	}
	family := "whole"
	switch {
	case strings.HasSuffix(key, ":scopes"):
		family = "catalog"
	case strings.Contains(key, ":latest:"):
		family = "latest"
	default:
		for _, suffix := range []string{"meta", "nodes", "leases", "intents", "runs", "messages", "sequence", "dags"} {
			if strings.HasSuffix(key, ":"+suffix) {
				family = suffix
				break
			}
		}
	}
	var read, written int64
	switch name {
	case "get", "hget":
		if result, ok := cmd.(*redis.StringCmd); ok && result.Err() == nil {
			read = int64(len(result.Val()))
		}
	case "hgetall":
		if result, ok := cmd.(*redis.MapStringStringCmd); ok && result.Err() == nil {
			for _, v := range result.Val() {
				read += int64(len(v))
			}
		}
	case "hmget":
		if result, ok := cmd.(*redis.SliceCmd); ok && result.Err() == nil {
			for _, v := range result.Val() {
				if v != nil {
					read += sessionPayloadLen(v)
				}
			}
		}
	case "smembers", "zrange", "zrevrange", "zrangebyscore", "zrevrangebyscore":
		if result, ok := cmd.(*redis.StringSliceCmd); ok && result.Err() == nil {
			for _, v := range result.Val() {
				read += int64(len(v))
			}
		}
		if result, ok := cmd.(*redis.ZSliceCmd); ok && result.Err() == nil {
			for _, v := range result.Val() {
				read += sessionPayloadLen(v.Member) + int64(len(strconv.FormatFloat(v.Score, 'g', -1, 64)))
			}
		}
	case "scard", "zcard", "hlen":
		if result, ok := cmd.(*redis.IntCmd); ok && result.Err() == nil {
			read = int64(len(strconv.FormatInt(result.Val(), 10)))
		}
	case "sismember":
		if result, ok := cmd.(*redis.BoolCmd); ok && result.Err() == nil {
			read = 1
		}
	case "zscore":
		if result, ok := cmd.(*redis.FloatCmd); ok && result.Err() == nil {
			read = int64(len(strconv.FormatFloat(result.Val(), 'g', -1, 64)))
		}
	}
	switch name {
	case "set":
		if len(args) > 2 {
			written = sessionPayloadLen(args[2])
		}
	case "hset":
		for i := 3; i < len(args); i += 2 {
			written += sessionPayloadLen(args[i])
		}
	case "sadd", "srem", "zrem":
		for _, v := range args[2:] {
			written += sessionPayloadLen(v)
		}
	case "zadd":
		for i := 3; i < len(args); i += 2 {
			written += sessionPayloadLen(args[i])
		}
	}
	c.stats.readValues[family] += read
	c.stats.writeValues[family] += written
	if name == "get" {
		c.stats.getBytes += read
	}
	if name == "set" {
		c.stats.setBytes += written
	}
	if c.trace {
		field := ""
		if len(args) > 2 {
			field = fmt.Sprint(args[2])
		}
		c.events = append(c.events, sessionCommandEvent{name: name, key: key, field: field, written: written})
	}
}

func (c *sessionCommandCounter) DialHook(next redis.DialHook) redis.DialHook { return next }
func (c *sessionCommandCounter) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error { err := next(ctx, cmd); c.record(cmd); return err }
}
func (c *sessionCommandCounter) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		err := next(ctx, cmds)
		c.mu.Lock()
		c.stats.pipelines++
		if errors.Is(err, redis.TxFailedErr) {
			c.stats.conflicts++
		}
		c.mu.Unlock()
		for _, cmd := range cmds {
			c.record(cmd)
		}
		return err
	}
}
func sessionCountCommands(t testing.TB, f *sessionFixture) *sessionCommandCounter {
	t.Helper()
	c := &sessionCommandCounter{}
	if clock, ok := f.mr.(interface{ interceptsTime() bool }); ok {
		c.timeIntercepted = clock.interceptsTime()
	}
	c.reset()
	client := redis.NewClient(&redis.Options{Addr: f.mr.Addr(), MaxRetries: -1})
	client.AddHook(c)
	f.mr.configureClient(client)
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(t.Context()).Err(); err != nil {
		t.Fatal(err)
	}
	f.repo.client = client
	c.reset()
	return c
}
func (s sessionCommandStats) report(b *testing.B) {
	for name, n := range s.commands {
		b.ReportMetric(float64(n)/float64(b.N), name+"/op")
	}
	var read, written int64
	for family, n := range s.readValues {
		read += n
		if n > 0 {
			b.ReportMetric(float64(n)/float64(b.N), family+"-read-B/op")
		}
	}
	for family, n := range s.writeValues {
		written += n
		if n > 0 {
			b.ReportMetric(float64(n)/float64(b.N), family+"-write-B/op")
		}
	}
	b.ReportMetric(float64(read)/float64(b.N), "ValueRead-B/op")
	b.ReportMetric(float64(written)/float64(b.N), "ValueWrite-B/op")
	b.ReportMetric(float64(s.logical)/float64(b.N), "logical-cmd/op")
	b.ReportMetric(float64(s.wire)/float64(b.N), "wire-cmd/op")
	b.ReportMetric(float64(s.wireTime)/float64(b.N), "wire-time/op")
	b.ReportMetric(float64(s.pipelines)/float64(b.N), "pipeline/op")
	b.ReportMetric(float64(s.conflicts)/float64(b.N), "conflict/op")
}

func sessionBenchmarkCapture() session.TurnCapture {
	var media schema.Message
	if err := json.Unmarshal([]byte(`{"role":"user","content":"fallback media","user_input_multi_content":[{"type":"image_url","image":{"base64data":"`+strings.Repeat("A", 16384)+`","mime_type":"image/png"}}]}`), &media); err != nil {
		panic(err)
	}
	capture := sessionCapture("current input")
	capture.Bootstrap = session.History(schema.UserMessage(strings.Repeat("fallback text ", 128)), &media, schema.AssistantMessage("fallback answer", nil))
	return capture
}
func sessionBenchmarkState(scope session.Scope, now int64, shape string, count int) sessionState {
	state := emptySessionState(scope)
	for i := range count {
		dagNumber := i + 1
		if shape != "roots" {
			dagNumber = 1
		}
		dagID := fmt.Sprintf("%032x", dagNumber)
		d := state.DAGs[dagID]
		if d == nil {
			d = &sessionDAG{ID: dagID, Generation: fmt.Sprintf("%032x", dagNumber+10000), State: sessionDAGActive, LastActive: now, Nodes: map[string]session.Node{}, Intents: map[string]session.Intent{}, Leases: map[string]session.Lease{}}
			state.DAGs[dagID] = d
		}
		n := session.Node{Scope: scope, Ref: session.NodeRef{DAGID: dagID, NodeID: fmt.Sprintf("%032x", i+20000)}, Agent: "A", RunID: fmt.Sprintf("%032x", i+30000), ReplyMessageIDs: []int{i + 101}, Version: session.Version, CommitSequence: int64(i + 1), Digest: strings.Repeat("a", 64), Size: 20000}
		n.FileName = n.Ref.NodeID + ".jsonl"
		if shape != "roots" && i > 0 {
			parent := i - 1 + 20000
			if shape == "fork" {
				parent = 20000 + (i-1)/2
			}
			n.Parent = &session.NodeRef{DAGID: dagID, NodeID: fmt.Sprintf("%032x", parent)}
		}
		d.Nodes[n.Ref.NodeID] = n
		state.Runs[n.RunID] = n.Ref
		state.Messages[strconv.Itoa(i+101)] = n.Ref
	}
	state.Sequence = int64(count)
	return state
}
func sessionSeedBenchmark(t testing.TB, f *sessionFixture, roots int) {
	t.Helper()
	sessionPartitionFixture(t, f, "roots", roots)
}
