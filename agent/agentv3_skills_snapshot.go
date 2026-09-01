//go:build !386 && !arm

package agentv3

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

func newAgentV3SkillSnapshot(source agentV3SkillSource, descriptors []agentV3SkillDescriptor) (agentV3SkillSnapshot, error) {
	if !isAgentV3SkillSource(source) {
		return agentV3SkillSnapshot{}, fmt.Errorf("%w: %q", errAgentV3InvalidSkillSource, source)
	}
	if len(descriptors) > agentV3SkillsMaxCount {
		return agentV3SkillSnapshot{}, fmt.Errorf("%w: %d skills exceeds the maximum of %d", errAgentV3InvalidSkillSnapshot, len(descriptors), agentV3SkillsMaxCount)
	}

	seen := make(map[string]struct{}, len(descriptors))
	skills := make([]agentV3SkillDescriptor, 0, len(descriptors))
	for _, descriptor := range descriptors {
		canonicalName, err := parseAgentV3CanonicalSkillName(descriptor.Name)
		if err != nil {
			return agentV3SkillSnapshot{}, err
		}
		if canonicalName != descriptor.Name {
			return agentV3SkillSnapshot{}, fmt.Errorf("%w: %q must already be canonical", errAgentV3InvalidSkillName, descriptor.Name)
		}
		if _, ok := seen[descriptor.Name]; ok {
			return agentV3SkillSnapshot{}, fmt.Errorf("%w: duplicate name %q", errAgentV3InvalidSkillSnapshot, descriptor.Name)
		}
		seen[descriptor.Name] = struct{}{}

		clone := cloneAgentV3SkillDescriptor(descriptor)
		clone.Source = source
		clone.SHA256 = agentV3SkillContentSHA256(clone.Content)
		skills = append(skills, clone)
	}
	sort.Slice(skills, func(i, j int) bool {
		return skills[i].Name < skills[j].Name
	})

	snapshot := agentV3SkillSnapshot{SchemaVersion: agentV3SkillSchemaVersion, SnapshotSHA256: agentV3SkillSnapshotSHA256(agentV3SkillSchemaVersion, skills), Skills: skills}
	return validateAgentV3SkillSnapshot(snapshot, source)
}

func emptyAgentV3SkillSnapshot(source agentV3SkillSource) agentV3SkillSnapshot {
	snapshot, err := newAgentV3SkillSnapshot(source, nil)
	if err != nil {
		panic(err)
	}
	return snapshot
}

func validateAgentV3SkillSnapshot(snapshot agentV3SkillSnapshot, expectedSource agentV3SkillSource) (agentV3SkillSnapshot, error) {
	if !isAgentV3SkillSource(expectedSource) {
		return agentV3SkillSnapshot{}, fmt.Errorf("%w: %q", errAgentV3InvalidSkillSource, expectedSource)
	}
	if snapshot.SchemaVersion != agentV3SkillSchemaVersion {
		return agentV3SkillSnapshot{}, fmt.Errorf("%w: schema version %d", errAgentV3InvalidSkillSnapshot, snapshot.SchemaVersion)
	}
	if len(snapshot.Skills) > agentV3SkillsMaxCount {
		return agentV3SkillSnapshot{}, fmt.Errorf("%w: %d skills exceeds the maximum of %d", errAgentV3InvalidSkillSnapshot, len(snapshot.Skills), agentV3SkillsMaxCount)
	}

	totalContentBytes := 0
	validated := make([]agentV3SkillDescriptor, 0, len(snapshot.Skills))
	previousName := ""
	for index, descriptor := range snapshot.Skills {
		if index > 0 && previousName >= descriptor.Name {
			return agentV3SkillSnapshot{}, fmt.Errorf("%w: skills are not strictly sorted at %q", errAgentV3InvalidSkillSnapshot, descriptor.Name)
		}
		previousName = descriptor.Name

		if !utf8.ValidString(descriptor.Name) || !utf8.ValidString(descriptor.Description) || !utf8.ValidString(descriptor.Content) || !utf8.ValidString(descriptor.SHA256) || !utf8.ValidString(string(descriptor.Source)) || !utf8.ValidString(descriptor.VirtualPath) {
			return agentV3SkillSnapshot{}, fmt.Errorf("%w: skill %q contains invalid UTF-8", errAgentV3InvalidSkillContent, descriptor.Name)
		}
		name, err := parseAgentV3CanonicalSkillName(descriptor.Name)
		if err != nil {
			return agentV3SkillSnapshot{}, err
		}
		if name != descriptor.Name {
			return agentV3SkillSnapshot{}, fmt.Errorf("%w: %q must already be canonical", errAgentV3InvalidSkillName, descriptor.Name)
		}
		if descriptor.Source != expectedSource {
			return agentV3SkillSnapshot{}, fmt.Errorf("%w: skill %q source %q does not match %q", errAgentV3InvalidSkillSource, descriptor.Name, descriptor.Source, expectedSource)
		}

		expectedPath := agentV3SkillVirtualPath(expectedSource, descriptor.Name)
		if descriptor.VirtualPath != expectedPath {
			return agentV3SkillSnapshot{}, fmt.Errorf("%w: skill %q virtual path %q", errAgentV3InvalidSkillSnapshot, descriptor.Name, descriptor.VirtualPath)
		}
		if descriptor.Content == "" {
			return agentV3SkillSnapshot{}, fmt.Errorf("%w: skill %q content is empty", errAgentV3InvalidSkillContent, descriptor.Name)
		}
		if len(descriptor.Content) > agentV3SkillFileMaxBytes {
			return agentV3SkillSnapshot{}, fmt.Errorf("%w: skill %q content exceeds %d bytes", errAgentV3InvalidSkillContent, descriptor.Name, agentV3SkillFileMaxBytes)
		}
		if totalContentBytes > agentV3SkillsMaxContentBytes-len(descriptor.Content) {
			return agentV3SkillSnapshot{}, fmt.Errorf("%w: aggregate content exceeds %d bytes", errAgentV3InvalidSkillContent, agentV3SkillsMaxContentBytes)
		}
		totalContentBytes += len(descriptor.Content)
		if descriptor.SHA256 != agentV3SkillContentSHA256(descriptor.Content) {
			return agentV3SkillSnapshot{}, fmt.Errorf("%w: skill %q content hash", errAgentV3InvalidSkillSnapshot, descriptor.Name)
		}
		if err := validateAgentV3FilesystemSkillDescription(expectedSource, descriptor); err != nil {
			return agentV3SkillSnapshot{}, err
		}

		validated = append(validated, cloneAgentV3SkillDescriptor(descriptor))
	}
	if snapshot.SnapshotSHA256 != agentV3SkillSnapshotSHA256(snapshot.SchemaVersion, validated) {
		return agentV3SkillSnapshot{}, fmt.Errorf("%w: snapshot hash", errAgentV3InvalidSkillSnapshot)
	}
	return agentV3SkillSnapshot{SchemaVersion: snapshot.SchemaVersion, SnapshotSHA256: strings.Clone(snapshot.SnapshotSHA256), Skills: validated}, nil
}

func mergeAgentV3SkillSnapshots(snapshots ...agentV3SkillSnapshot) (agentV3SkillCatalog, []agentV3SkillShadow, error) {
	candidatesByName := make(map[string][]agentV3SkillDescriptor)
	for _, snapshot := range snapshots {
		source := agentV3SkillSourceBuiltin
		if len(snapshot.Skills) > 0 {
			source = snapshot.Skills[0].Source
		}
		validated, err := validateAgentV3SkillSnapshot(snapshot, source)
		if err != nil {
			return agentV3SkillCatalog{}, nil, err
		}
		for _, descriptor := range validated.Skills {
			candidatesByName[descriptor.Name] = append(candidatesByName[descriptor.Name], cloneAgentV3SkillDescriptor(descriptor))
		}
	}

	names := make([]string, 0, len(candidatesByName))
	for name := range candidatesByName {
		names = append(names, name)
	}
	sort.Strings(names)

	catalog := agentV3SkillCatalog{ByName: make(map[string]agentV3SkillDescriptor, len(names)), Sorted: make([]agentV3SkillDescriptor, 0, len(names))}
	shadows := make([]agentV3SkillShadow, 0)
	for _, name := range names {
		candidates := candidatesByName[name]
		seenSources := make(map[agentV3SkillSource]struct{}, len(candidates))
		for _, candidate := range candidates {
			if _, ok := seenSources[candidate.Source]; ok {
				return agentV3SkillCatalog{}, nil, fmt.Errorf("%w: duplicate %q from source %q", errAgentV3InvalidSkillSnapshot, name, candidate.Source)
			}
			seenSources[candidate.Source] = struct{}{}
		}
		sort.Slice(candidates, func(i, j int) bool {
			return agentV3SkillSourcePriority(candidates[i].Source) > agentV3SkillSourcePriority(candidates[j].Source)
		})

		winner := cloneAgentV3SkillDescriptor(candidates[0])
		catalog.ByName[name] = cloneAgentV3SkillDescriptor(winner)
		catalog.Sorted = append(catalog.Sorted, cloneAgentV3SkillDescriptor(winner))
		for _, loser := range candidates[1:] {
			shadows = append(shadows, agentV3SkillShadow{Name: strings.Clone(name), Winner: cloneAgentV3SkillDescriptor(winner), Loser: cloneAgentV3SkillDescriptor(loser)})
		}
	}
	catalog.SnapshotSHA256 = agentV3SkillSnapshotSHA256(agentV3SkillSchemaVersion, catalog.Sorted)
	return catalog, shadows, nil
}

func agentV3SkillSnapshotSHA256(schemaVersion int, sorted []agentV3SkillDescriptor) string {
	hash := sha256.New()
	write := func(value string) {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(value))
	}
	write(strconv.Itoa(schemaVersion))
	write(strconv.Itoa(len(sorted)))
	for _, descriptor := range sorted {
		write(descriptor.Name)
		write(descriptor.Description)
		write(descriptor.Content)
		write(descriptor.SHA256)
		write(string(descriptor.Source))
		write(descriptor.VirtualPath)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func agentV3SkillContentSHA256(content string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
}

func agentV3SkillVirtualPath(source agentV3SkillSource, name string) string {
	if source == agentV3SkillSourceBuiltin {
		return ""
	}
	return "/skills/" + name + "/SKILL.md"
}
