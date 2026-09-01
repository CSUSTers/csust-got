//go:build 386 || arm

package agentv3

import (
	"context"

	"csust-got/config"

	tb "gopkg.in/telebot.v3"
)

// Init is a no-op on the existing 32-bit agent v3 stub.
func Init(_ context.Context) error { return nil }

// Close is a no-op on 386.
func Close() {}

// Chat is a no-op on 386.
func Chat(_ tb.Context, _ *config.AgentConfig, _ *config.AgentTrigger) error { return nil }

// HasCompiledAgent always returns false on 386.
func HasCompiledAgent(_ string) bool { return false }

func MemoryCommand(_ tb.Context) error { return nil }

func TraceLastCommand(_ tb.Context) error { return nil }

func ContextCacheCommand(_ tb.Context) error { return nil }

func RuntimeStatusCommand(_ tb.Context) error { return nil }

func RuntimeResetCommand(_ tb.Context) error { return nil }
