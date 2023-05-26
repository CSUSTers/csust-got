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
		{"hugefiver", "hugefiver"},
		{"hugeer", "hugeer"},
		{"huger", "hugeF**Ker"},
		{"", "HUGEFIVER"},
		{"even", "hugevener"},
		{"ray", "hugerayer"},
		{"wu", "hugewuer"},
		{"e", "hugeF**Ker"},
		{"en", "hugener"},
		{"py", "hugepier"},
	}
	for _, tt := range tests {
		got := hooEncode(tt.args)
		require.Equalf(t, tt.want, got, "encode(%s)", tt.args)
	}
}

func Test_decode(t *testing.T) {
	tests := []struct {
		args string
		want string
	}{
		{"hugefiver", "hugefiver"},
		{"hugeer", "hugeFAKEr"},
		{"huger", "hugeFAKEr"},
		{"", "hugeFAKEr"},
		{"even", "hugeFAKEr"},
		{"hugehugefiver", "hugefiver"},
		{"hugefiverer", "hugefiver"},
		{"hugehugefiverer", "hugefiver"},
		{"hugehugeer", "hugeer"},
	}
	for _, tt := range tests {
		got := hooDecode(tt.args)
		require.Equalf(t, tt.want, got, "decode(%s)", tt.args)
	}
}
