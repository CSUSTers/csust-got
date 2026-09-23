package agentv3

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"csust-got/config"
)

const agentV3SkillEnvMaxBytes = 8 * 1024

var errAgentV3SkillEnvInvalid = errors.New("agent v3 skill environment is invalid")

func readAgentV3SkillEnvironment(root *os.Root, name string, hooks *agentV3FilesystemSkillLoadHooks) (map[string]string, error) {
	info, err := root.Lstat(".env")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lstat .env for %q: %w", name, errAgentV3SkillEnvInvalid)
	}
	label := fmt.Sprintf(".env for %q", name)
	if err := validateAgentV3FilesystemSkillIdentity(label, true, info); err != nil {
		return nil, err
	}
	if info.Size() > agentV3SkillEnvMaxBytes {
		return nil, errAgentV3SkillEnvInvalid
	}
	if hooks != nil && hooks.afterEnvCheck != nil {
		hooks.afterEnvCheck(name)
	}
	file, err := openAgentV3SkillEnvironment(root)
	if err != nil {
		return nil, errAgentV3SkillEnvInvalid
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return nil, errAgentV3SkillEnvInvalid
	}
	post, err := root.Lstat(".env")
	if err != nil {
		return nil, errAgentV3SkillEnvInvalid
	}
	if err := validateAgentV3FilesystemSkillIdentity(label, true, info, opened, post); err != nil {
		return nil, err
	}
	if opened.Size() > agentV3SkillEnvMaxBytes {
		return nil, errAgentV3SkillEnvInvalid
	}
	if hooks != nil && hooks.beforeEnvRead != nil {
		hooks.beforeEnvRead(name)
	}
	data, err := io.ReadAll(io.LimitReader(file, agentV3SkillEnvMaxBytes+1))
	if err != nil || len(data) > agentV3SkillEnvMaxBytes {
		return nil, errAgentV3SkillEnvInvalid
	}
	return parseAgentV3SkillDotenv(data)
}

func parseAgentV3SkillDotenv(data []byte) (map[string]string, error) {
	if len(data) > agentV3SkillEnvMaxBytes || !utf8.Valid(data) || strings.ContainsRune(string(data), '\x00') {
		return nil, errAgentV3SkillEnvInvalid
	}
	values := make(map[string]string)
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSuffix(raw, "\r")
		if strings.ContainsRune(line, '\r') {
			return nil, errAgentV3SkillEnvInvalid
		}
		line = strings.Trim(line, " \t")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, errAgentV3SkillEnvInvalid
		}
		name, value = strings.Trim(name, " \t"), strings.Trim(value, " \t")
		if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") {
			if len(value) < 2 || value[len(value)-1] != value[0] {
				return nil, errAgentV3SkillEnvInvalid
			}
			value = value[1 : len(value)-1]
		} else if strings.HasSuffix(value, "\"") || strings.HasSuffix(value, "'") {
			return nil, errAgentV3SkillEnvInvalid
		}
		if _, exists := values[name]; exists {
			return nil, errAgentV3SkillEnvInvalid
		}
		values[name] = value
		if err := config.ValidateAgentV3RuntimeEnv(values); err != nil {
			return nil, errAgentV3SkillEnvInvalid
		}
	}
	return values, nil
}
