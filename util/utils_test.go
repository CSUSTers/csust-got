package util

import (
	"github.com/stretchr/testify/assert"
	tb "gopkg.in/telebot.v3"
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

func TestGetAllReplyMessagesText(t *testing.T) {
	type args struct {
		m *tb.Message
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "normal test",
			args: args{
				&tb.Message{
					ReplyTo: &tb.Message{
						ReplyTo: &tb.Message{
							ReplyTo: &tb.Message{
								Text: "test4",
							},
							Text: "test3",
						},
						Text: "test2",
					},
					Text: "test1",
				},
			},
			want: "test2\ntest3\ntest4\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, GetAllReplyMessagesText(tt.args.m), "GetAllReplyMessagesText(%v)", tt.args.m)
		})
	}
}

func TestUTF16Len(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{"empty", "", 0},
		{"ascii", "hello", 5},
		{"cjk is one unit each", "你好", 2},
		{"astral emoji is two units", "😀", 2},
		{"mixed", "a😀b", 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, UTF16Len(tt.text))
		})
	}
}

func TestFloodRetryAfterIgnoresOtherErrors(t *testing.T) {
	for _, err := range []error{nil, tb.ErrTooLongMessage, tb.ErrMessageNotModified} {
		wait, flooded := FloodRetryAfter(err)
		assert.False(t, flooded)
		assert.Zero(t, wait)
	}
}
