//go:build 386 || arm

package chatv2

import (
	"context"

	"csust-got/config"

	tb "gopkg.in/telebot.v3"
)

// Init is a no-op on 386: eino/sonic does not support 32-bit.
func Init(_ context.Context) error { return nil }

// Close is a no-op on 386.
func Close() {}

// Chat is a no-op on 386.
func Chat(_ tb.Context, _ *config.ChatConfigSingle, _ *config.ChatTrigger) error { return nil }