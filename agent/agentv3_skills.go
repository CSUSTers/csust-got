package agentv3

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	agentV3SkillSchemaVersion    = 1
	agentV3SkillFileMaxBytes     = 64 * 1024
	agentV3SkillsMaxCount        = 128
	agentV3SkillsMaxContentBytes = 1024 * 1024
)

type agentV3SkillSource string

const (
	agentV3SkillSourceBuiltin       agentV3SkillSource = "builtin"
	agentV3SkillSourceBotLocal      agentV3SkillSource = "bot-local"
	agentV3SkillSourceRuntimeGlobal agentV3SkillSource = "runtime-global"
)

type agentV3SkillDescriptor struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Content     string             `json:"content"`
	SHA256      string             `json:"sha256"`
	Source      agentV3SkillSource `json:"source"`
	VirtualPath string             `json:"virtual_path"`
}

type agentV3SkillSnapshot struct {
	SchemaVersion  int                      `json:"schema_version"`
	SnapshotSHA256 string                   `json:"snapshot_sha256"`
	Skills         []agentV3SkillDescriptor `json:"skills"`
}

type agentV3SkillCatalog struct {
	ByName         map[string]agentV3SkillDescriptor
	Sorted         []agentV3SkillDescriptor
	SnapshotSHA256 string
}

type agentV3SkillShadow struct {
	Name   string
	Winner agentV3SkillDescriptor
	Loser  agentV3SkillDescriptor
}

var (
	errAgentV3InvalidSkillName       = errors.New("agent v3 skill name is not canonical")
	errAgentV3InvalidSkillSource     = errors.New("agent v3 skill source is invalid")
	errAgentV3InvalidSkillSnapshot   = errors.New("agent v3 skill snapshot is invalid")
	errAgentV3InvalidSkillContent    = errors.New("agent v3 skill content is invalid")
	agentV3CanonicalSkillNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

func parseAgentV3CanonicalSkillName(raw string) (string, error) {
	name := normalizeAgentV3SkillName(raw)
	if !agentV3CanonicalSkillNamePattern.MatchString(name) {
		return "", fmt.Errorf("%w: %q", errAgentV3InvalidSkillName, raw)
	}
	return name, nil
}

func isAgentV3SkillSource(source agentV3SkillSource) bool {
	return agentV3SkillSourcePriority(source) != 0
}

func agentV3SkillSourcePriority(source agentV3SkillSource) int {
	switch source {
	case agentV3SkillSourceBuiltin:
		return 3
	case agentV3SkillSourceBotLocal:
		return 2
	case agentV3SkillSourceRuntimeGlobal:
		return 1
	default:
		return 0
	}
}

func cloneAgentV3SkillDescriptor(descriptor agentV3SkillDescriptor) agentV3SkillDescriptor {
	return agentV3SkillDescriptor{
		Name:        strings.Clone(descriptor.Name),
		Description: strings.Clone(descriptor.Description),
		Content:     strings.Clone(descriptor.Content),
		SHA256:      strings.Clone(descriptor.SHA256),
		Source:      agentV3SkillSource(strings.Clone(string(descriptor.Source))),
		VirtualPath: strings.Clone(descriptor.VirtualPath),
	}
}
