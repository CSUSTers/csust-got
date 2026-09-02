package agentv3

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

func loadAgentV3FilesystemSkillSnapshot(root string, source agentV3SkillSource) (agentV3SkillSnapshot, error) {
	return loadAgentV3FilesystemSkillSnapshotWithHooks(root, source, nil)
}

type agentV3FilesystemSkillLoadHooks struct {
	afterRootCheck     func()
	afterDirectoryRead func(int)
	afterChildCheck    func(string)
	afterSkillCheck    func(string)
	beforeSkillRead    func(string)
}

func loadAgentV3FilesystemSkillSnapshotWithHooks(root string, source agentV3SkillSource, hooks *agentV3FilesystemSkillLoadHooks) (agentV3SkillSnapshot, error) {
	if !isAgentV3FilesystemSkillSource(source) {
		return agentV3SkillSnapshot{}, fmt.Errorf("%w: filesystem loader cannot use %q", errAgentV3InvalidSkillSource, source)
	}
	if err := validateAgentV3FilesystemSkillRootPath(root); err != nil {
		return agentV3SkillSnapshot{}, err
	}
	preRootInfo, err := os.Lstat(root)
	if err != nil {
		return agentV3SkillSnapshot{}, fmt.Errorf("lstat skills root: %w", err)
	}
	if err := validateAgentV3FilesystemSkillIdentity("skills root", false, preRootInfo); err != nil {
		return agentV3SkillSnapshot{}, err
	}
	if hooks != nil && hooks.afterRootCheck != nil {
		hooks.afterRootCheck()
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return agentV3SkillSnapshot{}, fmt.Errorf("open skills root: %w", err)
	}
	defer func() { _ = rootHandle.Close() }()
	openedRootInfo, err := agentV3FilesystemSkillDirectoryInfo(rootHandle)
	if err != nil {
		return agentV3SkillSnapshot{}, fmt.Errorf("stat opened skills root: %w", err)
	}
	postRootInfo, err := os.Lstat(root)
	if err != nil {
		return agentV3SkillSnapshot{}, fmt.Errorf("lstat skills root after opening: %w", err)
	}
	if err := validateAgentV3FilesystemSkillIdentity("skills root", false, preRootInfo, openedRootInfo, postRootInfo); err != nil {
		return agentV3SkillSnapshot{}, err
	}

	directory, err := rootHandle.Open(".")
	if err != nil {
		return agentV3SkillSnapshot{}, fmt.Errorf("open skills root directory: %w", err)
	}
	defer func() { _ = directory.Close() }()
	descriptors := make([]agentV3SkillDescriptor, 0, agentV3SkillsMaxCount)
	totalContentBytes := 0
	for {
		entries, readErr := directory.ReadDir(1)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return agentV3SkillSnapshot{}, fmt.Errorf("read skills root: %w", readErr)
		}
		if len(entries) == 0 {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return agentV3SkillSnapshot{}, errAgentV3FilesystemSkillEmptyDirectoryBatch
		}
		if hooks != nil && hooks.afterDirectoryRead != nil {
			hooks.afterDirectoryRead(len(entries))
		}
		entry := entries[0]
		entryInfo, err := rootHandle.Lstat(entry.Name())
		if err != nil {
			return agentV3SkillSnapshot{}, fmt.Errorf("lstat skill entry %q: %w", entry.Name(), err)
		}
		if entryInfo.Mode().IsRegular() {
			continue
		}
		if err := validateAgentV3FilesystemSkillIdentity(fmt.Sprintf("skill entry %q", entry.Name()), false, entryInfo); err != nil {
			return agentV3SkillSnapshot{}, err
		}
		if len(descriptors) == agentV3SkillsMaxCount {
			return agentV3SkillSnapshot{}, agentV3FilesystemSkillCountLimitError(len(descriptors) + 1)
		}
		descriptor, err := loadAgentV3FilesystemSkill(rootHandle, entry.Name(), entryInfo, agentV3SkillsMaxContentBytes-totalContentBytes, hooks)
		if err != nil {
			return agentV3SkillSnapshot{}, err
		}
		descriptors = append(descriptors, descriptor)
		totalContentBytes += len(descriptor.Content)
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return newAgentV3SkillSnapshot(source, descriptors)
}

func loadAgentV3FilesystemSkill(root *os.Root, entryName string, preChildInfo os.FileInfo, remainingContentBytes int, hooks *agentV3FilesystemSkillLoadHooks) (agentV3SkillDescriptor, error) {
	name, err := parseAgentV3CanonicalSkillName(entryName)
	if err != nil {
		return agentV3SkillDescriptor{}, err
	}
	if name != entryName {
		return agentV3SkillDescriptor{}, fmt.Errorf("%w: directory name %q must already be canonical", errAgentV3InvalidSkillName, entryName)
	}
	if hooks != nil && hooks.afterChildCheck != nil {
		hooks.afterChildCheck(name)
	}
	childRoot, err := root.OpenRoot(entryName)
	if err != nil {
		return agentV3SkillDescriptor{}, fmt.Errorf("open skill entry %q: %w", name, err)
	}
	defer func() { _ = childRoot.Close() }()
	openedChildInfo, err := agentV3FilesystemSkillDirectoryInfo(childRoot)
	if err != nil {
		return agentV3SkillDescriptor{}, fmt.Errorf("stat opened skill entry %q: %w", name, err)
	}
	postChildInfo, err := root.Lstat(entryName)
	if err != nil {
		return agentV3SkillDescriptor{}, fmt.Errorf("lstat skill entry %q after opening: %w", name, err)
	}
	if err := validateAgentV3FilesystemSkillIdentity(fmt.Sprintf("skill entry %q", name), false, preChildInfo, openedChildInfo, postChildInfo); err != nil {
		return agentV3SkillDescriptor{}, err
	}
	skillInfo, err := childRoot.Lstat("SKILL.md")
	if err != nil {
		return agentV3SkillDescriptor{}, fmt.Errorf("lstat SKILL.md for %q: %w", name, err)
	}
	if err := validateAgentV3FilesystemSkillIdentity(fmt.Sprintf("SKILL.md for %q", name), true, skillInfo); err != nil {
		return agentV3SkillDescriptor{}, err
	}
	if err := validateAgentV3FilesystemSkillContentBudget(skillInfo.Size(), remainingContentBytes); err != nil {
		return agentV3SkillDescriptor{}, err
	}
	if hooks != nil && hooks.afterSkillCheck != nil {
		hooks.afterSkillCheck(name)
	}
	content, err := readAgentV3SkillFile(childRoot, name, skillInfo, remainingContentBytes, hooks)
	if err != nil {
		return agentV3SkillDescriptor{}, err
	}
	contentString := string(content)
	description, err := agentV3SkillDescription(contentString)
	if err != nil {
		return agentV3SkillDescriptor{}, fmt.Errorf("skill %q: %w", name, err)
	}
	return agentV3SkillDescriptor{Name: name, Description: description, Content: contentString, VirtualPath: "/skills/" + name + "/SKILL.md"}, nil
}

func readAgentV3SkillFile(root *os.Root, name string, preSkillInfo os.FileInfo, remainingContentBytes int, hooks *agentV3FilesystemSkillLoadHooks) ([]byte, error) {
	file, err := root.Open("SKILL.md")
	if err != nil {
		return nil, fmt.Errorf("read SKILL.md for %q: %w", name, err)
	}
	defer func() { _ = file.Close() }()
	openedSkillInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat opened SKILL.md for %q: %w", name, err)
	}
	postSkillInfo, err := root.Lstat("SKILL.md")
	if err != nil {
		return nil, fmt.Errorf("lstat SKILL.md for %q after opening: %w", name, err)
	}
	if err := validateAgentV3FilesystemSkillIdentity(fmt.Sprintf("SKILL.md for %q", name), true, preSkillInfo, openedSkillInfo, postSkillInfo); err != nil {
		return nil, err
	}
	if openedSkillInfo.Size() > agentV3SkillFileMaxBytes {
		return nil, fmt.Errorf("%w: SKILL.md for %q exceeds %d bytes", errAgentV3InvalidSkillContent, name, agentV3SkillFileMaxBytes)
	}
	if err := validateAgentV3FilesystemSkillContentBudget(openedSkillInfo.Size(), remainingContentBytes); err != nil {
		return nil, err
	}
	if hooks != nil && hooks.beforeSkillRead != nil {
		hooks.beforeSkillRead(name)
	}
	readLimit := agentV3SkillFileMaxBytes + 1
	if remainingContentBytes < agentV3SkillFileMaxBytes {
		readLimit = remainingContentBytes + 1
	}
	content, err := io.ReadAll(io.LimitReader(file, int64(readLimit)))
	if err != nil {
		return nil, fmt.Errorf("read SKILL.md for %q: %w", name, err)
	}
	if len(content) > agentV3SkillFileMaxBytes {
		return nil, fmt.Errorf("%w: SKILL.md for %q exceeds %d bytes", errAgentV3InvalidSkillContent, name, agentV3SkillFileMaxBytes)
	}
	if err := validateAgentV3FilesystemSkillContentBudget(int64(len(content)), remainingContentBytes); err != nil {
		return nil, err
	}
	return content, nil
}

func validateAgentV3FilesystemSkillDescription(source agentV3SkillSource, descriptor agentV3SkillDescriptor) error {
	if !isAgentV3FilesystemSkillSource(source) {
		return nil
	}
	description, err := agentV3SkillDescription(descriptor.Content)
	if err != nil {
		return fmt.Errorf("skill %q: %w", descriptor.Name, err)
	}
	if descriptor.Description != description {
		return fmt.Errorf("%w: skill %q description does not match its content", errAgentV3InvalidSkillSnapshot, descriptor.Name)
	}
	return nil
}

func agentV3SkillDescription(content string) (string, error) {
	if !utf8.ValidString(content) {
		return "", fmt.Errorf("%w: invalid UTF-8", errAgentV3InvalidSkillContent)
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || isAgentV3ATXHeading(line) {
			continue
		}
		runes := []rune(line)
		if len(runes) > 200 {
			return string(runes[:200]), nil
		}
		return line, nil
	}
	return "", fmt.Errorf("%w: missing prose description", errAgentV3InvalidSkillContent)
}

func isAgentV3ATXHeading(line string) bool {
	count := 0
	for count < len(line) && line[count] == '#' {
		count++
	}
	if count == 0 || count > 6 {
		return false
	}
	if count == len(line) {
		return true
	}
	runeAfterMarker, _ := utf8.DecodeRuneInString(line[count:])
	return unicode.IsSpace(runeAfterMarker)
}

func isAgentV3FilesystemSkillSource(source agentV3SkillSource) bool {
	return source == agentV3SkillSourceBotLocal || source == agentV3SkillSourceRuntimeGlobal
}
