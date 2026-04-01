//go:build !386 && !arm

package chatv2

import (
	"testing"
	"time"

	"csust-got/config"

	"github.com/stretchr/testify/assert"
)

func TestGetEditInterval(t *testing.T) {
	tests := []struct {
		name   string
		format *config.ChatOutputFormatConfig
		want   time.Duration
	}{
		{
			name:   "empty defaults to one second",
			format: &config.ChatOutputFormatConfig{},
			want:   time.Second,
		},
		{
			name: "invalid duration defaults to one second",
			format: &config.ChatOutputFormatConfig{
				EditInterval: "not-a-duration",
			},
			want: time.Second,
		},
		{
			name: "custom duration is respected",
			format: &config.ChatOutputFormatConfig{
				EditInterval: "2.5s",
			},
			want: 2500 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, getEditInterval(tt.format))
		})
	}
}
