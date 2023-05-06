// Package entities provides an abstraction of tg bot entities.
package entities

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_splitText(t *testing.T) {
	tests := []struct {
		name string
		txt  string
		want []string
	}{
		{
			"Empty String",
			"",
			nil,
		},
		{
			"Split Count: 1",
			"abc",
			[]string{"abc"},
		},
		{
			"Split Count: 3",
			"love and peace",
			[]string{
				"love",
				"and",
				"peace",
			},
		},
		{
			"Use Mixed Space and Tab as Sep",
			"love \tand\t peace",
			[]string{
				"love",
				"and",
				"peace",
			},
		},
		{
			"Use Unicode",
			"惊了 摸了 还蛮怪的",
			[]string{
				"惊了",
				"摸了",
				"还蛮怪的",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitText(tt.txt, -1)
			require.Equal(t, tt.want, got)
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("split limit 2", func(t *testing.T) {
		got := splitText("love and peace", 2)
		assert.Equal(t, []string{"love", "and peace"}, got)
	})
}
