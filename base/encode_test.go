package base

import (
	"strings"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func Test_encode(t *testing.T) {
	tests := []struct {
		args string
		want string
	}{
		{"", "h0oOo"},
		{"0", "h0oOo"},
		{"Hoo", "h0oOo"},
		{"h0o0o00", "h0oOo"},
		{"FAKER", "h0oFAKEROo"},
		{"h0o0o0OFAKER", "h0oFAKEROo"},
		{"FAKERo0oO0", "h0oFAKEROo"},
		{"h0o0o0OFAKERo0oO0", "h0oFAKEROo"},

		// case from Hoo self
		{"h0oaOo", "h0oaOo"},
	}
	replacer := strings.NewReplacer(lo.FlatMap[string, string]([]string{"0", "o", "O"}, func(item string, _ int) []string { return []string{item, "o"} })...)
	for _, tt := range tests {
		got := hooEncode(tt.args)
		require.Equalf(t, replacer.Replace(tt.want), replacer.Replace(got), "encode(%s)", tt.args)
	}
}
