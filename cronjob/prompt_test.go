package cronjob

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validPrompt = `## Context
The task has all required background.
## Steps
1. Inspect the current state.
2. Produce the result.
## Goal
Return a concise summary.`

func TestValidatePrompt(t *testing.T) {
	require.NoError(t, ValidatePrompt(validPrompt, len(validPrompt)))

	tests := []struct {
		name   string
		prompt string
		limit  int
	}{
		{name: "invalid limit", prompt: validPrompt, limit: 0},
		{name: "invalid UTF-8", prompt: string([]byte{0xff}), limit: 100},
		{name: "too large", prompt: validPrompt, limit: len(validPrompt) - 1},
		{name: "missing section", prompt: "## Context\na\n## Steps\nb", limit: 100},
		{name: "empty section", prompt: "## Context\na\n## Steps\n \n## Goal\nc", limit: 100},
		{name: "wrong order", prompt: "## Steps\na\n## Context\nb\n## Goal\nc", limit: 100},
		{name: "duplicate", prompt: "## Context\na\n## Steps\nb\n## Context\nc\n## Goal\nd", limit: 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePrompt(tt.prompt, tt.limit)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidArgument)
		})
	}
}
