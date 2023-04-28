package util

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestCheckUrl(t *testing.T) {
	type args struct {
		url string
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "200 normal test",
			args: args{"https://www.baidu.com"},
			want: true,
		},
		{
			name: "404 test 1",
			args: args{"https://www.baidu.com/404"},
			want: false,
		},
		{
			name: "404 test 2",
			args: args{"https://s.csu.st/c404cc"},
			want: false,
		},
		{
			name: "invalid url test",
			args: args{"https://google.baidu.com/404/"},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, CheckUrl(tt.args.url), "CheckUrl(%v)", tt.args.url)
		})
	}
}
