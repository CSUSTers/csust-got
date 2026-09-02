package agentv3

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var errAgentV3FilesystemSkillEmptyDirectoryBatch = errors.New("read skills root: empty directory batch")

func validateAgentV3FilesystemSkillRootPath(root string) error {
	if root == "" || filepath.Base(root) == "." || filepath.Base(root) == ".." || filepath.Clean(root) != root {
		return fmt.Errorf("%w: skills root path is not canonical", errAgentV3InvalidSkillSnapshot)
	}
	return nil
}

func agentV3FilesystemSkillCountLimitError(count int) error {
	return fmt.Errorf("%w: %d skills exceeds the maximum of %d", errAgentV3InvalidSkillSnapshot, count, agentV3SkillsMaxCount)
}

func validateAgentV3FilesystemSkillContentBudget(size int64, remaining int) error {
	if size > int64(remaining) {
		return fmt.Errorf("%w: aggregate content exceeds %d bytes", errAgentV3InvalidSkillContent, agentV3SkillsMaxContentBytes)
	}
	return nil
}

func agentV3FilesystemSkillDirectoryInfo(root *os.Root) (os.FileInfo, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	return directory.Stat()
}

func validateAgentV3FilesystemSkillIdentity(label string, regular bool, infos ...os.FileInfo) error {
	for _, info := range infos {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s is a symlink", errAgentV3InvalidSkillSnapshot, label)
		}
		if regular && !info.Mode().IsRegular() {
			return fmt.Errorf("%w: %s is not a regular file", errAgentV3InvalidSkillSnapshot, label)
		}
		if !regular && !info.IsDir() {
			return fmt.Errorf("%w: %s is not a directory", errAgentV3InvalidSkillSnapshot, label)
		}
	}
	for _, info := range infos[1:] {
		if !os.SameFile(infos[0], info) {
			return fmt.Errorf("%w: %s changed while opening", errAgentV3InvalidSkillSnapshot, label)
		}
	}
	return nil
}
