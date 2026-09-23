package agentv3

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentV3SkillDotenvLiteralContract(t *testing.T) {
	values, err := parseAgentV3SkillDotenv([]byte("# comment\r\n KEY = 'plain $TOKEN = #raw'\nOTHER=\"a\\nb\"\nEMPTY=\n"))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"KEY": "plain $TOKEN = #raw", "OTHER": `a\nb`, "EMPTY": ""}, values)
	for _, raw := range []string{
		"KEY=no\nKEY=duplicate", "LD_PRELOAD=value", "PATH=hidden", "bad-name=value",
		"KEY='unterminated", "KEY=unopened'", "KEY=value\rno", "KEY\n", "KEY=\x00", "KEY=\xff",
		"KEY=" + strings.Repeat("x", 2049), "KEY=" + strings.Repeat("x", agentV3SkillEnvMaxBytes),
	} {
		_, err := parseAgentV3SkillDotenv([]byte(raw))
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "hidden")
	}
}

func TestAgentV3SkillEnvironmentFrozenAndNotSerialized(t *testing.T) {
	root := t.TempDir()
	writeSkillTestFile(t, filepath.Join(root, "alpha", "SKILL.md"), alphaSkillContent)
	envFile := filepath.Join(root, "alpha", ".env")
	writeSkillTestFile(t, envFile, "KEY=first\n")
	snapshot, err := loadAgentV3FilesystemSkillSnapshot(root, agentV3SkillSourceBotLocal)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"KEY": "first"}, snapshot.Skills[0].environment)
	serialized, err := json.Marshal(snapshot)
	require.NoError(t, err)
	assert.NotContains(t, string(serialized), "first")
	assert.NotContains(t, fmt.Sprintf("%+v %#v", snapshot, snapshot), "first")
	assert.Equal(t, alphaContentSHA, snapshot.Skills[0].SHA256)
	writeSkillTestFile(t, envFile, "KEY=second\n")
	clone := cloneAgentV3SkillSnapshotForChat(snapshot)
	assert.Equal(t, "first", clone.Skills[0].environment["KEY"])
	clone.Skills[0].environment["KEY"] = "changed"
	assert.Equal(t, "first", snapshot.Skills[0].environment["KEY"])
}

func TestAgentV3SkillEnvironmentRejectsSymlinkAndSwap(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		writeSkillTestFile(t, filepath.Join(root, "alpha", "SKILL.md"), alphaSkillContent)
		outside := filepath.Join(root, "secret")
		writeSkillTestFile(t, outside, "KEY=secret\n")
		createSkillTestSymlink(t, outside, filepath.Join(root, "alpha", ".env"))
		_, err := loadAgentV3FilesystemSkillSnapshot(root, agentV3SkillSourceBotLocal)
		require.Error(t, err)
	})
	t.Run("swap", func(t *testing.T) {
		root := t.TempDir()
		writeSkillTestFile(t, filepath.Join(root, "alpha", "SKILL.md"), alphaSkillContent)
		original := filepath.Join(root, "alpha", ".env")
		writeSkillTestFile(t, original, "KEY=ok\n")
		outside := filepath.Join(root, "alpha", "attacker")
		writeSkillTestFile(t, outside, "KEY=secret\n")
		_, err := loadAgentV3FilesystemSkillSnapshotWithHooks(root, agentV3SkillSourceBotLocal, &agentV3FilesystemSkillLoadHooks{
			afterEnvCheck: func(string) { swapSkillTestPathWithSymlink(t, original, outside) },
		})
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "secret")
	})
	t.Run("oversize", func(t *testing.T) {
		root := t.TempDir()
		writeSkillTestFile(t, filepath.Join(root, "alpha", "SKILL.md"), alphaSkillContent)
		require.NoError(t, os.WriteFile(filepath.Join(root, "alpha", ".env"), []byte(strings.Repeat("x", agentV3SkillEnvMaxBytes+1)), 0o600))
		_, err := loadAgentV3FilesystemSkillSnapshot(root, agentV3SkillSourceBotLocal)
		require.Error(t, err)
	})
}

func TestAgentV3SkillEnvironmentRejectsFIFOAfterCheckWithoutBlocking(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("FIFO replacement test requires Linux")
	}
	const child = "AGENTV3_TEST_ENV_FIFO_CHILD"
	if os.Getenv(child) != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAgentV3SkillEnvironmentRejectsFIFOAfterCheckWithoutBlocking$")
		cmd.Env = append(os.Environ(), child+"=1")
		output, err := cmd.CombinedOutput()
		require.NotEqual(t, context.DeadlineExceeded, ctx.Err(), "opening swapped FIFO blocked: %s", output)
		require.NoError(t, err, "%s", output)
		return
	}
	root := t.TempDir()
	writeSkillTestFile(t, filepath.Join(root, "alpha", "SKILL.md"), alphaSkillContent)
	original := filepath.Join(root, "alpha", ".env")
	writeSkillTestFile(t, original, "KEY=ok\n")
	fifo := filepath.Join(root, "alpha", "fifo")
	require.NoError(t, exec.Command("mkfifo", fifo).Run())
	_, err := loadAgentV3FilesystemSkillSnapshotWithHooks(root, agentV3SkillSourceBotLocal, &agentV3FilesystemSkillLoadHooks{
		afterEnvCheck: func(string) {
			require.NoError(t, os.Rename(original, original+".old"))
			require.NoError(t, os.Rename(fifo, original))
		},
	})
	require.ErrorIs(t, err, errAgentV3InvalidSkillSnapshot)
}
