package base

import (
	"reflect"
	"testing"
)

func Test_parseVoiceArgsArray(t *testing.T) {
	type arg = string
	tests := []struct {
		name         string
		arg          arg
		wantMargs    [][2]string
		wantRestArgs []string
		wantOk       bool
	}{
		{
			"simple text",
			"123",
			[][2]string{},
			[]string{"123"},
			true,
		},
		{
			"multi args",
			"\n123   角色=凯亚\t\n\n 性别=\t 456  ",
			[][2]string{
				{"角色", "凯亚"},
				{"性别", ""},
			},
			[]string{"123", "456"},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMargs, gotRestArgs, gotOk := parseVoiceArgsArray(tt.arg)
			if !reflect.DeepEqual(gotMargs, tt.wantMargs) {
				t.Errorf("parseVoiceArgsArray() gotMargs = %v, want %v", gotMargs, tt.wantMargs)
			}
			if !reflect.DeepEqual(gotRestArgs, tt.wantRestArgs) {
				t.Errorf("parseVoiceArgsArray() gotRestArgs = %v, want %v", gotRestArgs, tt.wantRestArgs)
			}
			if gotOk != tt.wantOk {
				t.Errorf("parseVoiceArgsArray() gotOk = %v, want %v", gotOk, tt.wantOk)
			}
		})
	}
}
