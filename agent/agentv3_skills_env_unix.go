//go:build unix

package agentv3

import (
	"os"
	"syscall"
)

func openAgentV3SkillEnvironment(root *os.Root) (*os.File, error) {
	return root.OpenFile(".env", os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
