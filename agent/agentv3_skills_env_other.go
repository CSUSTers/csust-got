//go:build !unix

package agentv3

import "os"

func openAgentV3SkillEnvironment(root *os.Root) (*os.File, error) {
	return root.Open(".env")
}
