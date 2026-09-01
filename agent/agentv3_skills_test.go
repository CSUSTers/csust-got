//go:build !386 && !arm

package agentv3

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unicode/utf8"
)

const (
	alphaSkillContent = "# Alpha\nAlpha skill.\n"
	alphaContentSHA   = "1fbaf47fc271ddf43f40756a9a3d2776156e7e2c6472bf9bf4cd66ea143be574"
	alphaSnapshotSHA  = "66d894d641ce04fcc04eaec3837a0dac24a27dd5ee9160ce8d3871ce0155f9ee"
)

func TestLoadAgentV3FilesystemSkillSnapshotDirectChildrenAndLimits(t *testing.T) {
	root := t.TempDir()
	writeSkillTestFile(t, filepath.Join(root, "README.md"), "ignored root file")
	writeSkillTestFile(t, filepath.Join(root, "alpha", "SKILL.md"), alphaSkillContent)
	writeSkillTestFile(t, filepath.Join(root, "alpha", "scripts", "tool.sh"), "#!/bin/sh\n")
	writeSkillTestFile(t, filepath.Join(root, "alpha", "nested", "ignored", "SKILL.md"), "not discovered")

	snapshot, err := loadAgentV3FilesystemSkillSnapshot(root, agentV3SkillSourceRuntimeGlobal)
	if err != nil {
		t.Fatalf("load direct children: %v", err)
	}
	if snapshot.SchemaVersion != agentV3SkillSchemaVersion {
		t.Fatalf("schema version = %d, want %d", snapshot.SchemaVersion, agentV3SkillSchemaVersion)
	}
	if len(snapshot.Skills) != 1 {
		t.Fatalf("skill count = %d, want 1", len(snapshot.Skills))
	}
	got := snapshot.Skills[0]
	if got.Name != "alpha" || got.Description != "Alpha skill." || got.Content != alphaSkillContent || got.SHA256 != alphaContentSHA || got.Source != agentV3SkillSourceRuntimeGlobal || got.VirtualPath != "/skills/alpha/SKILL.md" {
		t.Fatalf("unexpected descriptor: %#v", got)
	}

	t.Run("64 KiB accepted and 64 KiB plus one rejected", func(t *testing.T) {
		acceptedRoot := t.TempDir()
		writeSkillTestFile(t, filepath.Join(acceptedRoot, "accepted", "SKILL.md"), skillContentOfBytes(agentV3SkillFileMaxBytes))
		if _, err := loadAgentV3FilesystemSkillSnapshot(acceptedRoot, agentV3SkillSourceBotLocal); err != nil {
			t.Fatalf("load 64 KiB skill: %v", err)
		}

		rejectedRoot := t.TempDir()
		writeSkillTestFile(t, filepath.Join(rejectedRoot, "rejected", "SKILL.md"), skillContentOfBytes(agentV3SkillFileMaxBytes+1))
		if _, err := loadAgentV3FilesystemSkillSnapshot(rejectedRoot, agentV3SkillSourceBotLocal); err == nil {
			t.Fatal("64 KiB plus one skill was accepted")
		}
	})

	t.Run("128 skills accepted and 129 rejected", func(t *testing.T) {
		acceptedRoot := t.TempDir()
		writeSkillFixtures(t, acceptedRoot, agentV3SkillsMaxCount, 32)
		accepted, err := loadAgentV3FilesystemSkillSnapshot(acceptedRoot, agentV3SkillSourceBotLocal)
		if err != nil {
			t.Fatalf("load 128 skills: %v", err)
		}
		if len(accepted.Skills) != agentV3SkillsMaxCount {
			t.Fatalf("accepted count = %d, want %d", len(accepted.Skills), agentV3SkillsMaxCount)
		}

		rejectedRoot := t.TempDir()
		writeSkillFixtures(t, rejectedRoot, agentV3SkillsMaxCount+1, 32)
		if _, err := loadAgentV3FilesystemSkillSnapshot(rejectedRoot, agentV3SkillSourceBotLocal); err == nil {
			t.Fatal("129 skills were accepted")
		}
	})

	t.Run("one MiB aggregate accepted and overflow rejected", func(t *testing.T) {
		acceptedRoot := t.TempDir()
		writeSkillFixtures(t, acceptedRoot, agentV3SkillsMaxContentBytes/agentV3SkillFileMaxBytes, agentV3SkillFileMaxBytes)
		if _, err := loadAgentV3FilesystemSkillSnapshot(acceptedRoot, agentV3SkillSourceBotLocal); err != nil {
			t.Fatalf("load one MiB aggregate: %v", err)
		}

		rejectedRoot := t.TempDir()
		writeSkillFixtures(t, rejectedRoot, agentV3SkillsMaxContentBytes/agentV3SkillFileMaxBytes, agentV3SkillFileMaxBytes)
		writeSkillTestFile(t, filepath.Join(rejectedRoot, "overflow", "SKILL.md"), "Description\n")
		if _, err := loadAgentV3FilesystemSkillSnapshot(rejectedRoot, agentV3SkillSourceBotLocal); err == nil {
			t.Fatal("aggregate content over one MiB was accepted")
		}
	})
}

func TestLoadAgentV3FilesystemSkillSnapshotRejectsSymlinksAndMalformedEntries(t *testing.T) {
	t.Run("root symlink lexical forms", func(t *testing.T) {
		parent := t.TempDir()
		target := filepath.Join(parent, "target")
		writeSkillTestFile(t, filepath.Join(target, "alpha", "SKILL.md"), alphaSkillContent)
		link := filepath.Join(parent, "root-link")
		createSkillTestSymlink(t, target, link)

		for _, test := range []struct {
			name string
			root string
		}{
			{name: "raw", root: link},
			{name: "trailing separator", root: link + string(filepath.Separator)},
			{name: "terminal dot", root: link + string(filepath.Separator) + "."},
		} {
			t.Run(test.name, func(t *testing.T) {
				if _, err := loadAgentV3FilesystemSkillSnapshot(test.root, agentV3SkillSourceBotLocal); err == nil {
					t.Fatal("root symlink was accepted")
				}
			})
		}
	})

	t.Run("direct child symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		writeSkillTestFile(t, filepath.Join(target, "SKILL.md"), alphaSkillContent)
		createSkillTestSymlink(t, target, filepath.Join(root, "alpha"))
		if _, err := loadAgentV3FilesystemSkillSnapshot(root, agentV3SkillSourceBotLocal); err == nil {
			t.Fatal("direct child symlink was accepted")
		}
	})

	t.Run("SKILL.md symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target.md")
		writeSkillTestFile(t, target, alphaSkillContent)
		link := filepath.Join(root, "alpha", "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatalf("make skill directory: %v", err)
		}
		createSkillTestSymlink(t, target, link)
		if _, err := loadAgentV3FilesystemSkillSnapshot(root, agentV3SkillSourceBotLocal); err == nil {
			t.Fatal("SKILL.md symlink was accepted")
		}
	})

	for _, test := range []struct {
		name  string
		setup func(root string)
	}{
		{
			name: "noncanonical direct directory",
			setup: func(root string) {
				writeSkillTestFile(t, filepath.Join(root, "Alpha_Name", "SKILL.md"), alphaSkillContent)
			},
		},
		{
			name: "missing SKILL.md",
			setup: func(root string) {
				if err := os.Mkdir(filepath.Join(root, "alpha"), 0o755); err != nil {
					t.Fatalf("make skill directory: %v", err)
				}
			},
		},
		{
			name: "nonregular SKILL.md",
			setup: func(root string) {
				if err := os.MkdirAll(filepath.Join(root, "alpha", "SKILL.md"), 0o755); err != nil {
					t.Fatalf("make nonregular SKILL.md: %v", err)
				}
			},
		},
		{
			name: "invalid UTF-8",
			setup: func(root string) {
				writeSkillTestBytes(t, filepath.Join(root, "alpha", "SKILL.md"), []byte("# Heading\n\xff\n"))
			},
		},
		{
			name: "empty content",
			setup: func(root string) {
				writeSkillTestFile(t, filepath.Join(root, "alpha", "SKILL.md"), "")
			},
		},
		{
			name: "no prose description",
			setup: func(root string) {
				writeSkillTestFile(t, filepath.Join(root, "alpha", "SKILL.md"), "# Heading\n## Still a heading\n")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.setup(root)
			if _, err := loadAgentV3FilesystemSkillSnapshot(root, agentV3SkillSourceBotLocal); err == nil {
				t.Fatal("malformed skill entry was accepted")
			}
		})
	}
}

func TestLoadAgentV3FilesystemSkillSnapshotLimitsBeforeRejectedSkillHooks(t *testing.T) {
	t.Run("129th skill", func(t *testing.T) {
		root := t.TempDir()
		writeSkillFixtures(t, root, agentV3SkillsMaxCount+1, 32)
		var childChecks, skillChecks, reads int

		_, err := loadAgentV3FilesystemSkillSnapshotWithHooks(root, agentV3SkillSourceBotLocal, &agentV3FilesystemSkillLoadHooks{
			afterChildCheck: func(string) { childChecks++ },
			afterSkillCheck: func(string) { skillChecks++ },
			beforeSkillRead: func(string) { reads++ },
		})
		if err == nil {
			t.Fatal("129 skills were accepted")
		}
		if childChecks != agentV3SkillsMaxCount || skillChecks != agentV3SkillsMaxCount || reads != agentV3SkillsMaxCount {
			t.Fatalf("rejected skill hooks = child:%d skill:%d read:%d, want all %d", childChecks, skillChecks, reads, agentV3SkillsMaxCount)
		}
	})

	t.Run("aggregate overflow skill", func(t *testing.T) {
		root := t.TempDir()
		writeSkillFixtures(t, root, agentV3SkillsMaxContentBytes/agentV3SkillFileMaxBytes+1, agentV3SkillFileMaxBytes)
		var childChecks, skillChecks, reads int

		_, err := loadAgentV3FilesystemSkillSnapshotWithHooks(root, agentV3SkillSourceBotLocal, &agentV3FilesystemSkillLoadHooks{
			afterChildCheck: func(string) { childChecks++ },
			afterSkillCheck: func(string) { skillChecks++ },
			beforeSkillRead: func(string) { reads++ },
		})
		if err == nil {
			t.Fatal("aggregate content overflow was accepted")
		}
		if childChecks != agentV3SkillsMaxContentBytes/agentV3SkillFileMaxBytes+1 {
			t.Fatalf("child checks = %d, want %d", childChecks, agentV3SkillsMaxContentBytes/agentV3SkillFileMaxBytes+1)
		}
		if skillChecks != agentV3SkillsMaxContentBytes/agentV3SkillFileMaxBytes || reads != agentV3SkillsMaxContentBytes/agentV3SkillFileMaxBytes {
			t.Fatalf("overflow skill was inspected or read: skill:%d read:%d", skillChecks, reads)
		}
	})
}

func TestLoadAgentV3FilesystemSkillSnapshotStreamsRootDirectory(t *testing.T) {
	root := t.TempDir()
	for index := range 512 {
		writeSkillTestFile(t, filepath.Join(root, "ignored-"+strconv.Itoa(index)), "ignored")
	}
	writeSkillTestFile(t, filepath.Join(root, "alpha", "SKILL.md"), alphaSkillContent)
	var batchSizes []int

	snapshot, err := loadAgentV3FilesystemSkillSnapshotWithHooks(root, agentV3SkillSourceBotLocal, &agentV3FilesystemSkillLoadHooks{
		afterDirectoryRead: func(size int) { batchSizes = append(batchSizes, size) },
	})
	if err != nil {
		t.Fatalf("stream root directory: %v", err)
	}
	if len(snapshot.Skills) != 1 || snapshot.Skills[0].Name != "alpha" {
		t.Fatalf("unexpected streamed snapshot: %#v", snapshot.Skills)
	}
	if len(batchSizes) < 2 {
		t.Fatalf("directory read batches = %d, want multiple", len(batchSizes))
	}
	for _, size := range batchSizes {
		if size != 1 {
			t.Fatalf("directory read batch size = %d, want 1", size)
		}
	}
}

func TestValidateAgentV3FilesystemSkillRootPath(t *testing.T) {
	separator := string(filepath.Separator)
	for _, test := range []struct {
		name  string
		root  string
		valid bool
	}{
		{name: "canonical relative", root: "skills", valid: true},
		{name: "canonical temporary absolute", root: t.TempDir(), valid: true},
		{name: "filesystem root", root: separator, valid: true},
		{name: "empty", root: ""},
		{name: "trailing separator", root: "skills" + separator},
		{name: "terminal current directory", root: "skills" + separator + "."},
		{name: "terminal parent directory", root: "skills" + separator + ".."},
		{name: "duplicate separator", root: "skills" + separator + separator + "nested"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateAgentV3FilesystemSkillRootPath(test.root)
			if (err == nil) != test.valid {
				t.Fatalf("validate root %q error = %v, want valid=%t", test.root, err, test.valid)
			}
		})
	}
}

func TestLoadAgentV3FilesystemSkillSnapshotRejectsSwapRaces(t *testing.T) {
	t.Run("root", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "root")
		attacker := filepath.Join(parent, "attacker")
		writeSkillTestFile(t, filepath.Join(root, "alpha", "SKILL.md"), alphaSkillContent)
		writeSkillTestFile(t, filepath.Join(attacker, "alpha", "SKILL.md"), "# Attacker\nAttacker content.\n")

		snapshot, err := loadAgentV3FilesystemSkillSnapshotWithHooks(root, agentV3SkillSourceBotLocal, &agentV3FilesystemSkillLoadHooks{
			afterRootCheck: func() { swapSkillTestPathWithSymlink(t, root, attacker) },
		})
		assertSkillTestSwapRejected(t, snapshot, err, "Attacker content.")
	})

	t.Run("direct child", func(t *testing.T) {
		root := t.TempDir()
		attacker := filepath.Join(root, "z-attacker")
		writeSkillTestFile(t, filepath.Join(root, "alpha", "SKILL.md"), alphaSkillContent)
		writeSkillTestFile(t, filepath.Join(attacker, "SKILL.md"), "# Attacker\nAttacker content.\n")

		snapshot, err := loadAgentV3FilesystemSkillSnapshotWithHooks(root, agentV3SkillSourceBotLocal, &agentV3FilesystemSkillLoadHooks{
			afterChildCheck: func(string) { swapSkillTestPathWithSymlink(t, filepath.Join(root, "alpha"), attacker) },
		})
		assertSkillTestSwapRejected(t, snapshot, err, "Attacker content.")
	})

	t.Run("SKILL.md", func(t *testing.T) {
		root := t.TempDir()
		attacker := filepath.Join(root, "alpha", "attacker.md")
		writeSkillTestFile(t, filepath.Join(root, "alpha", "SKILL.md"), alphaSkillContent)
		writeSkillTestFile(t, attacker, "# Attacker\nAttacker content.\n")

		snapshot, err := loadAgentV3FilesystemSkillSnapshotWithHooks(root, agentV3SkillSourceBotLocal, &agentV3FilesystemSkillLoadHooks{
			afterSkillCheck: func(string) { swapSkillTestPathWithSymlink(t, filepath.Join(root, "alpha", "SKILL.md"), attacker) },
		})
		assertSkillTestSwapRejected(t, snapshot, err, "Attacker content.")
	})

	t.Run("unchanged handles load", func(t *testing.T) {
		root := t.TempDir()
		writeSkillTestFile(t, filepath.Join(root, "alpha", "SKILL.md"), alphaSkillContent)

		snapshot, err := loadAgentV3FilesystemSkillSnapshotWithHooks(root, agentV3SkillSourceBotLocal, &agentV3FilesystemSkillLoadHooks{})
		if err != nil {
			t.Fatalf("load unchanged handles: %v", err)
		}
		if len(snapshot.Skills) != 1 || snapshot.Skills[0].Content != alphaSkillContent {
			t.Fatalf("unexpected unchanged snapshot: %#v", snapshot.Skills)
		}
	})
}

func TestParseAgentV3CanonicalSkillNameNormalizesThenValidates(t *testing.T) {
	for _, test := range []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{raw: " Alpha_Name ", want: "alpha-name"},
		{raw: "a--b", want: "a--b"},
		{raw: "", wantErr: true},
		{raw: "-alpha", wantErr: true},
		{raw: "alpha!", wantErr: true},
		{raw: "alpha name", wantErr: true},
		{raw: strings.Repeat("a", 65), wantErr: true},
		{raw: "技能", wantErr: true},
	} {
		t.Run(strconv.Quote(test.raw), func(t *testing.T) {
			got, err := parseAgentV3CanonicalSkillName(test.raw)
			if test.wantErr {
				if err == nil {
					t.Fatalf("parse(%q) = %q, nil error", test.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse(%q): %v", test.raw, err)
			}
			if got != test.want {
				t.Fatalf("parse(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

func TestAgentV3SkillDescriptionUsesFirstProseLineAndRuneLimit(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
		want    string
		wantErr bool
	}{
		{name: "skips headings and blank lines", content: "\n# Heading\n### More heading\n  First prose line  \nSecond line\n", want: "First prose line"},
		{name: "hash without whitespace is prose", content: "#not-heading\n", want: "#not-heading"},
		{name: "unicode whitespace after heading marker", content: "#\u00a0Heading\nDescription\n", want: "Description"},
		{name: "rune limit", content: "# Heading\n" + strings.Repeat("界", 201), want: strings.Repeat("界", 200)},
		{name: "empty", content: "", wantErr: true},
		{name: "whitespace only", content: " \n\t\n", wantErr: true},
		{name: "headings only", content: "# Heading\n###### Last\n", wantErr: true},
		{name: "invalid UTF-8", content: string([]byte{'\xff'}), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			descriptor := agentV3SkillDescriptor{
				Name:        "alpha",
				Description: test.want,
				Content:     test.content,
				VirtualPath: "/skills/alpha/SKILL.md",
			}
			snapshot, err := newAgentV3SkillSnapshot(agentV3SkillSourceBotLocal, []agentV3SkillDescriptor{descriptor})
			if test.wantErr {
				if err == nil {
					t.Fatal("invalid description input was accepted")
				}
				return
			}
			if err != nil {
				t.Fatalf("make snapshot: %v", err)
			}
			if len(snapshot.Skills) != 1 || snapshot.Skills[0].Description != test.want {
				t.Fatalf("description = %#v, want %q", snapshot.Skills, test.want)
			}
			if utf8.RuneCountInString(snapshot.Skills[0].Description) > 200 {
				t.Fatalf("description is longer than 200 runes: %q", snapshot.Skills[0].Description)
			}
		})
	}
}

func TestAgentV3SkillSnapshotHashMatchesCanonicalVector(t *testing.T) {
	descriptor := agentV3SkillDescriptor{
		Name:        "alpha",
		Description: "Alpha skill.",
		Content:     alphaSkillContent,
		SHA256:      alphaContentSHA,
		Source:      agentV3SkillSourceRuntimeGlobal,
		VirtualPath: "/skills/alpha/SKILL.md",
	}
	independent := sha256.Sum256(canonicalSkillBytesForTest(agentV3SkillSchemaVersion, []agentV3SkillDescriptor{descriptor}))
	if got := hex.EncodeToString(independent[:]); got != alphaSnapshotSHA {
		t.Fatalf("independent canonical hash = %s, want %s", got, alphaSnapshotSHA)
	}
	if got := agentV3SkillSnapshotSHA256(agentV3SkillSchemaVersion, []agentV3SkillDescriptor{descriptor}); got != alphaSnapshotSHA {
		t.Fatalf("snapshot hash = %s, want %s", got, alphaSnapshotSHA)
	}
}

func TestValidateAgentV3SkillSnapshotRejectsSameSourceDuplicatesAndHashMismatch(t *testing.T) {
	valid := testSkillSnapshot(agentV3SkillSourceBotLocal, []agentV3SkillDescriptor{
		{Name: "alpha", Description: "Alpha skill.", Content: alphaSkillContent, VirtualPath: "/skills/alpha/SKILL.md"},
		{Name: "beta", Description: "Beta skill.", Content: "# Beta\nBeta skill.\n", VirtualPath: "/skills/beta/SKILL.md"},
	})
	if _, err := validateAgentV3SkillSnapshot(valid, agentV3SkillSourceBotLocal); err != nil {
		t.Fatalf("validate valid snapshot: %v", err)
	}

	duplicate := testSkillSnapshot(agentV3SkillSourceBotLocal, []agentV3SkillDescriptor{
		{Name: "alpha", Description: "Alpha skill.", Content: alphaSkillContent, VirtualPath: "/skills/alpha/SKILL.md"},
		{Name: "alpha", Description: "Another alpha.", Content: "# Alpha\nAnother alpha.\n", VirtualPath: "/skills/alpha/SKILL.md"},
	})
	if _, err := validateAgentV3SkillSnapshot(duplicate, agentV3SkillSourceBotLocal); err == nil {
		t.Fatal("same-source duplicate names were accepted")
	}

	contentHashMismatch := valid
	contentHashMismatch.Skills = append([]agentV3SkillDescriptor(nil), valid.Skills...)
	contentHashMismatch.Skills[0].SHA256 = strings.Repeat("0", 64)
	contentHashMismatch.SnapshotSHA256 = testSkillSnapshotSHA(contentHashMismatch.SchemaVersion, contentHashMismatch.Skills)
	if _, err := validateAgentV3SkillSnapshot(contentHashMismatch, agentV3SkillSourceBotLocal); err == nil {
		t.Fatal("content hash mismatch was accepted")
	}

	snapshotHashMismatch := valid
	snapshotHashMismatch.SnapshotSHA256 = strings.Repeat("0", 64)
	if _, err := validateAgentV3SkillSnapshot(snapshotHashMismatch, agentV3SkillSourceBotLocal); err == nil {
		t.Fatal("snapshot hash mismatch was accepted")
	}
}

func TestMergeAgentV3SkillSnapshotsAppliesPrecedenceAndStableSort(t *testing.T) {
	runtimeGlobal := testSkillSnapshot(agentV3SkillSourceRuntimeGlobal, []agentV3SkillDescriptor{
		{Name: "alpha", Description: "Runtime alpha.", Content: "# Alpha\nRuntime alpha.\n", VirtualPath: "/skills/alpha/SKILL.md"},
		{Name: "beta", Description: "Runtime beta.", Content: "# Beta\nRuntime beta.\n", VirtualPath: "/skills/beta/SKILL.md"},
	})
	botLocal := testSkillSnapshot(agentV3SkillSourceBotLocal, []agentV3SkillDescriptor{
		{Name: "alpha", Description: "Bot alpha.", Content: "# Alpha\nBot alpha.\n", VirtualPath: "/skills/alpha/SKILL.md"},
	})
	builtin := testSkillSnapshot(agentV3SkillSourceBuiltin, []agentV3SkillDescriptor{
		{Name: "alpha", Description: "Builtin alpha.", Content: "Builtin alpha.", VirtualPath: ""},
		{Name: "gamma", Description: "Builtin gamma.", Content: "Builtin gamma.", VirtualPath: ""},
	})

	catalog, shadows, err := mergeAgentV3SkillSnapshots(runtimeGlobal, botLocal, builtin)
	if err != nil {
		t.Fatalf("merge snapshots: %v", err)
	}
	if names := agentV3SkillNames(catalog.Sorted); strings.Join(names, ",") != "alpha,beta,gamma" {
		t.Fatalf("sorted names = %v", names)
	}
	if got := catalog.ByName["alpha"].Source; got != agentV3SkillSourceBuiltin {
		t.Fatalf("alpha winner source = %q, want builtin", got)
	}
	if len(shadows) != 2 {
		t.Fatalf("shadow count = %d, want 2: %#v", len(shadows), shadows)
	}
	for _, shadow := range shadows {
		if shadow.Name != "alpha" || shadow.Winner.Source != agentV3SkillSourceBuiltin {
			t.Fatalf("unexpected shadow: %#v", shadow)
		}
	}
	if shadows[0].Loser.Source != agentV3SkillSourceBotLocal || shadows[1].Loser.Source != agentV3SkillSourceRuntimeGlobal {
		t.Fatalf("shadow loser sources = %q, %q", shadows[0].Loser.Source, shadows[1].Loser.Source)
	}
	if catalog.SnapshotSHA256 != testSkillSnapshotSHA(agentV3SkillSchemaVersion, catalog.Sorted) {
		t.Fatalf("catalog snapshot hash = %q, want canonical hash", catalog.SnapshotSHA256)
	}
}

func canonicalSkillBytesForTest(schemaVersion int, skills []agentV3SkillDescriptor) []byte {
	var buf bytes.Buffer
	write := func(value string) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len([]byte(value))))
		buf.Write(size[:])
		buf.WriteString(value)
	}
	write(strconv.Itoa(schemaVersion))
	write(strconv.Itoa(len(skills)))
	for _, skill := range skills {
		for _, value := range []string{skill.Name, skill.Description, skill.Content, skill.SHA256, string(skill.Source), skill.VirtualPath} {
			write(value)
		}
	}
	return buf.Bytes()
}

func testSkillSnapshot(source agentV3SkillSource, descriptors []agentV3SkillDescriptor) agentV3SkillSnapshot {
	skills := append([]agentV3SkillDescriptor(nil), descriptors...)
	for i := range skills {
		skills[i].Source = source
		hash := sha256.Sum256([]byte(skills[i].Content))
		skills[i].SHA256 = hex.EncodeToString(hash[:])
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return agentV3SkillSnapshot{
		SchemaVersion:  agentV3SkillSchemaVersion,
		SnapshotSHA256: testSkillSnapshotSHA(agentV3SkillSchemaVersion, skills),
		Skills:         skills,
	}
}

func testSkillSnapshotSHA(schemaVersion int, skills []agentV3SkillDescriptor) string {
	hash := sha256.Sum256(canonicalSkillBytesForTest(schemaVersion, skills))
	return hex.EncodeToString(hash[:])
}

func writeSkillFixtures(t *testing.T, root string, count, bytesPerSkill int) {
	t.Helper()
	for i := range count {
		writeSkillTestFile(t, filepath.Join(root, "skill-"+strconv.Itoa(i), "SKILL.md"), skillContentOfBytes(bytesPerSkill))
	}
}

func skillContentOfBytes(size int) string {
	const prefix = "# Heading\nDescription\n"
	if size < len(prefix) {
		panic("skill test content size is too small")
	}
	return prefix + strings.Repeat("x", size-len(prefix))
}

func writeSkillTestFile(t *testing.T, path, content string) {
	t.Helper()
	writeSkillTestBytes(t, path, []byte(content))
}

func writeSkillTestBytes(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("make parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func createSkillTestSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" && (errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.Errno(1314))) {
			t.Skipf("symlink creation unavailable: %v", err)
		}
		t.Fatalf("create symlink %s -> %s: %v", link, target, err)
	}
}

func swapSkillTestPathWithSymlink(t *testing.T, path, target string) {
	t.Helper()
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatalf("rename %s for swap: %v", path, err)
	}
	relativeTarget, err := filepath.Rel(filepath.Dir(path), target)
	if err != nil {
		t.Fatalf("make relative symlink target for %s: %v", path, err)
	}
	createSkillTestSymlink(t, relativeTarget, path)
}

func assertSkillTestSwapRejected(t *testing.T, snapshot agentV3SkillSnapshot, err error, attackerContent string) {
	t.Helper()
	if err == nil {
		t.Fatal("swapped skill path was accepted")
	}
	for _, skill := range snapshot.Skills {
		if strings.Contains(skill.Content, attackerContent) {
			t.Fatal("snapshot returned attacker content")
		}
	}
}

func agentV3SkillNames(skills []agentV3SkillDescriptor) []string {
	names := make([]string, len(skills))
	for i, skill := range skills {
		names[i] = skill.Name
	}
	return names
}
