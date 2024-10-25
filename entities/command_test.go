// Package entities provides an abstraction of tg bot entities.
package entities

import (
	"errors"
	"reflect"
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

func Test_CommandFromText(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		argc     int
		wantCmd  *BotCommand
		wantRest string
		wantErr  error
	}{
		{
			name: "simple command",
			text: "/hello",
			argc: -1,
			wantCmd: &BotCommand{
				name: "hello",
				args: []string{},
			},
			wantRest: "",
			wantErr:  nil,
		},
		{
			name: "command with args",
			text: "/hello world foo",
			argc: -1,
			wantCmd: &BotCommand{
				name: "hello",
				args: []string{"world", "foo"},
			},
			wantRest: "",
			wantErr:  nil,
		},
		{
			name: "unicode test",
			text: "/hello 你好 👋",
			argc: -1,
			wantCmd: &BotCommand{
				name: "hello",
				args: []string{"你好", "👋"},
			},
			wantRest: "",
			wantErr:  nil,
		},
		{
			name: "command with argc",
			text: "/hello world foo",
			argc: 1,
			wantCmd: &BotCommand{
				name: "hello",
				args: []string{"world"},
			},
			wantRest: "foo",
			wantErr:  nil,
		},
		{
			name:     "invalid command",
			text:     "hello world",
			argc:     -1,
			wantCmd:  nil,
			wantRest: "",
			wantErr:  errParseCommandName,
		},
		{
			name:     "empty text",
			text:     "",
			argc:     -1,
			wantCmd:  nil,
			wantRest: "",
			wantErr:  errParseCommand,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, rest, err := CommandFromText(tt.text, tt.argc)
			if !reflect.DeepEqual(cmd, tt.wantCmd) {
				t.Errorf("CommandFromText() cmd = %v, want %v", cmd, tt.wantCmd)
			}
			if rest != tt.wantRest {
				t.Errorf("CommandFromText() rest = %v, want %v", rest, tt.wantRest)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("CommandFromText() err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
