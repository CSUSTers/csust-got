package cronjob

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

var promptSections = []string{"## Context", "## Steps", "## Goal"}

// ValidatePrompt checks the required section order, content, encoding, and size.
func ValidatePrompt(prompt string, maxBytes int) error {
	if maxBytes <= 0 {
		return NewError(CodeInvalidArgument, "prompt byte limit must be positive")
	}
	if !utf8.ValidString(prompt) {
		return NewError(CodeInvalidArgument, "prompt must be valid UTF-8")
	}
	if len(prompt) > maxBytes {
		return NewError(CodeInvalidArgument, fmt.Sprintf("prompt exceeds %d bytes", maxBytes))
	}

	sections := make(map[string]string, len(promptSections))
	current := ""
	nextSection := 0
	for _, line := range strings.Split(prompt, "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		isRequiredHeading := false
		for _, heading := range promptSections {
			if trimmed != heading {
				continue
			}
			if _, exists := sections[heading]; exists {
				return NewError(CodeInvalidArgument, fmt.Sprintf("prompt section %s appears more than once", heading))
			}
			if nextSection >= len(promptSections) || promptSections[nextSection] != heading {
				return NewError(CodeInvalidArgument, "prompt sections must be ordered Context, Steps, Goal")
			}
			sections[heading] = ""
			current = heading
			nextSection++
			isRequiredHeading = true
			break
		}
		if !isRequiredHeading && current != "" {
			sections[current] += line + "\n"
		}
	}

	for _, heading := range promptSections {
		content, ok := sections[heading]
		if !ok {
			return NewError(CodeInvalidArgument, "prompt is missing "+heading)
		}
		if strings.TrimSpace(content) == "" {
			return NewError(CodeInvalidArgument, fmt.Sprintf("prompt section %s is empty", heading))
		}
	}
	return nil
}
