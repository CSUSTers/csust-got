package base

import (
	"testing"

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
	}
	for _, tt := range tests {
		got := hooEncode(tt.args)
		require.Equalf(t, tt.want, got, "encode(%s)", tt.args)
	}
}
