package gacha

import (
	"csust-got/config"
	"gopkg.in/telebot.v3"
	"reflect"
	"testing"
)

func Test_setGachaSession(t *testing.T) {
	type args struct {
		m *telebot.Message
	}
	tests := []struct {
		name    string
		args    args
		want    config.GachaTenant
		wantErr bool
	}{
		{
			name: "normal test",
			args: args{
				&telebot.Message{
					Text: "/gacha_setting {\"FiveStar\":{\"Counter\":0,\"Probability\":0.6,\"FailBackNum\":90},\"" +
						"FourStar\":{\"Counter\":0,\"Probability\":5.7,\"FailBackNum\":10},\"ID\":\"chat_1\"}",
				},
			},
			wantErr: false,
			want: config.GachaTenant{
				FiveStar: config.GachaInfo{
					Counter:     0,
					Probability: 0.6,
					FailBackNum: 90,
				},
				FourStar: config.GachaInfo{
					Counter:     0,
					Probability: 5.7,
					FailBackNum: 10,
				},
				ID: "chat_1",
			},
		},
		{
			name: "invalid json test",
			args: args{
				&telebot.Message{
					Text: "/gacha_setting {\"FiveStar\":{\"Counter\":0,\"Probability\":0.6,\"\"",
				},
			},
			wantErr: true,
			want: config.GachaTenant{
				FiveStar: config.GachaInfo{},
				FourStar: config.GachaInfo{},
				ID:       "",
			},
		},
		{
			name: "null test",
			args: args{
				&telebot.Message{
					Text: "/gacha_setting null",
				},
			},
			wantErr: true,
			want: config.GachaTenant{
				FiveStar: config.GachaInfo{},
				FourStar: config.GachaInfo{},
				ID:       "",
			},
		},
		{
			name: "empty test",
			args: args{
				&telebot.Message{
					Text: "/gacha_setting",
				},
			},
			wantErr: true,
			want: config.GachaTenant{
				FiveStar: config.GachaInfo{},
				FourStar: config.GachaInfo{},
				ID:       "",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := setGachaSession(tt.args.m)
			if (err != nil) != tt.wantErr {
				t.Errorf("setGachaSession() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("setGachaSession() got = %v, want %v", got, tt.want)
			}
		})
	}
}
