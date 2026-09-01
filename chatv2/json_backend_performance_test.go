//go:build !race && !386 && !arm

package chatv2

import (
	"encoding/json"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/require"
)

var (
	jsonBackendBytesSink   []byte
	jsonBackendPayloadSink jsonBackendPayload
	jsonBackendNodeSink    any
	jsonBackendValidSink   bool
)

func TestJSONBackendPerformanceGate(t *testing.T) {
	payload := representativeJSONBackendPayload(t)
	encoded, err := sonic.Marshal(payload)
	require.NoError(t, err)

	paimonMarshal := testing.Benchmark(func(b *testing.B) {
		benchmarkJSONBackendPaimonMarshal(b, payload)
	})
	standardMarshal := testing.Benchmark(func(b *testing.B) {
		benchmarkJSONBackendStandardMarshal(b, payload)
	})
	paimonUnmarshal := testing.Benchmark(func(b *testing.B) {
		benchmarkJSONBackendPaimonUnmarshal(b, encoded)
	})
	standardUnmarshal := testing.Benchmark(func(b *testing.B) {
		benchmarkJSONBackendStandardUnmarshal(b, encoded)
	})

	logJSONBackendBenchmarkResult(t, "Paimon marshal", paimonMarshal)
	logJSONBackendBenchmarkResult(t, "encoding/json marshal", standardMarshal)
	logJSONBackendBenchmarkResult(t, "Paimon unmarshal", paimonUnmarshal)
	logJSONBackendBenchmarkResult(t, "encoding/json unmarshal", standardUnmarshal)

	require.LessOrEqual(t, float64(paimonMarshal.NsPerOp()), float64(standardMarshal.NsPerOp())*2.5,
		"Paimon marshal must be no slower than 2.5x encoding/json")
	require.LessOrEqual(t, float64(paimonUnmarshal.NsPerOp()), float64(standardUnmarshal.NsPerOp())*2.5,
		"Paimon unmarshal must be no slower than 2.5x encoding/json")
}

func BenchmarkJSONBackend(b *testing.B) {
	payload := representativeJSONBackendPayload(b)
	encoded, err := sonic.Marshal(payload)
	if err != nil {
		b.Fatal(err)
	}
	encodedString := string(encoded)

	b.Run("PaimonMarshal", func(b *testing.B) {
		benchmarkJSONBackendPaimonMarshal(b, payload)
	})
	b.Run("StandardMarshal", func(b *testing.B) {
		benchmarkJSONBackendStandardMarshal(b, payload)
	})
	b.Run("PaimonUnmarshal", func(b *testing.B) {
		benchmarkJSONBackendPaimonUnmarshal(b, encoded)
	})
	b.Run("StandardUnmarshal", func(b *testing.B) {
		benchmarkJSONBackendStandardUnmarshal(b, encoded)
	})
	b.Run("PaimonGetFromString", func(b *testing.B) {
		benchmarkJSONBackendPaimonGetFromString(b, encodedString)
	})
	b.Run("PaimonValid", func(b *testing.B) {
		benchmarkJSONBackendPaimonValid(b, encoded)
	})
}

func benchmarkJSONBackendPaimonMarshal(b *testing.B, payload jsonBackendPayload) {
	b.ReportAllocs()
	for b.Loop() {
		encoded, err := sonic.Marshal(payload)
		if err != nil {
			b.Fatal(err)
		}
		jsonBackendBytesSink = encoded
	}
}

func benchmarkJSONBackendStandardMarshal(b *testing.B, payload jsonBackendPayload) {
	b.ReportAllocs()
	for b.Loop() {
		encoded, err := json.Marshal(payload)
		if err != nil {
			b.Fatal(err)
		}
		jsonBackendBytesSink = encoded
	}
}

func benchmarkJSONBackendPaimonUnmarshal(b *testing.B, encoded []byte) {
	b.ReportAllocs()
	for b.Loop() {
		var decoded jsonBackendPayload
		if err := sonic.Unmarshal(encoded, &decoded); err != nil {
			b.Fatal(err)
		}
		jsonBackendPayloadSink = decoded
	}
}

func benchmarkJSONBackendStandardUnmarshal(b *testing.B, encoded []byte) {
	b.ReportAllocs()
	for b.Loop() {
		var decoded jsonBackendPayload
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			b.Fatal(err)
		}
		jsonBackendPayloadSink = decoded
	}
}

func benchmarkJSONBackendPaimonGetFromString(b *testing.B, encoded string) {
	b.ReportAllocs()
	for b.Loop() {
		node, err := sonic.GetFromString(encoded, "message", "tool_calls", 0, "function", "arguments")
		if err != nil {
			b.Fatal(err)
		}
		jsonBackendNodeSink = node
	}
}

func benchmarkJSONBackendPaimonValid(b *testing.B, encoded []byte) {
	b.ReportAllocs()
	for b.Loop() {
		jsonBackendValidSink = sonic.Valid(encoded)
	}
}

func logJSONBackendBenchmarkResult(t *testing.T, name string, result testing.BenchmarkResult) {
	t.Helper()
	t.Logf("%s: %d ns/op, %d B/op, %d allocs/op", name, result.NsPerOp(), result.AllocedBytesPerOp(), result.AllocsPerOp())
}
