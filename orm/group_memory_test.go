package orm

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGroupMemoryCatalogTracksSortedDirectories(t *testing.T) {
	setupAgentV3Redis(t)
	ctx := t.Context()
	scope := AgentV3Scope{Bot: "bot", Platform: "tg", ChatID: -100}
	first := AgentV3MemoryItem{ID: "z-guide", Category: "campus", Directory: "library", Description: "Library guide", Content: "Opening hours"}
	second := AgentV3MemoryItem{ID: "a-guide", Category: "campus", Directory: "library", Description: "Later description", Content: "Study rooms"}
	for _, item := range []AgentV3MemoryItem{
		{ID: "notice", Category: "other", Directory: "access", Description: "Other access", Content: "Visitor policy"},
		first,
		{ID: "access", Category: "campus", Directory: "access", Description: "Campus access", Content: "Main entrance"},
		second,
	} {
		denial, err := AddGroupMemory(ctx, scope, item, nil)
		require.NoError(t, err)
		require.Empty(t, denial)
	}

	want := []GroupMemoryDirectory{
		{Category: "campus", Directory: "access", Description: "Campus access", IDs: []string{"access"}},
		{Category: "campus", Directory: "library", Description: first.Description, IDs: []string{second.ID, first.ID}},
		{Category: "other", Directory: "access", Description: "Other access", IDs: []string{"notice"}},
	}
	catalog, err := GroupMemoryCatalog(ctx, scope)
	require.NoError(t, err)
	require.Equal(t, want, catalog)

	read, err := ReadGroupMemory(ctx, scope, second.Category, second.Directory, second.ID)
	require.NoError(t, err)
	require.NotNil(t, read)
	require.Equal(t, first.Description, read.Description)
	require.Equal(t, second.Content, read.Content)

	denial, err := ChangeGroupMemory(ctx, scope, first.Category, first.Directory, first.ID, nil, nil)
	require.NoError(t, err)
	require.Empty(t, denial)
	want[1].IDs = []string{second.ID}
	catalog, err = GroupMemoryCatalog(ctx, scope)
	require.NoError(t, err)
	require.Equal(t, want, catalog)

	denial, err = ChangeGroupMemory(ctx, scope, second.Category, second.Directory, second.ID, nil, nil)
	require.NoError(t, err)
	require.Empty(t, denial)
	catalog, err = GroupMemoryCatalog(ctx, scope)
	require.NoError(t, err)
	require.Equal(t, []GroupMemoryDirectory{want[0], want[2]}, catalog)
	read, err = ReadGroupMemory(ctx, scope, second.Category, second.Directory, second.ID)
	require.NoError(t, err)
	require.Nil(t, read)

	denial, err = AddGroupMemory(ctx, scope, second, nil)
	require.NoError(t, err)
	require.Empty(t, denial)
	want[1].Description = second.Description
	catalog, err = GroupMemoryCatalog(ctx, scope)
	require.NoError(t, err)
	require.Equal(t, want, catalog)
}

func TestGroupMemoryEnforcesScopePathIDAndPermissions(t *testing.T) {
	setupAgentV3Redis(t)
	ctx := t.Context()
	scope := AgentV3Scope{Bot: "bot", Platform: "tg", ChatID: -100}
	otherScope := scope
	otherScope.ChatID = -200
	item := AgentV3MemoryItem{
		ID: "guide", Category: "campus", Directory: "library", Description: "Library guide",
		Content: "Opening hours", CreatedBy: 7,
	}
	denial, err := AddGroupMemory(ctx, scope, item, nil)
	require.NoError(t, err)
	require.Empty(t, denial)

	duplicate := item
	duplicate.Category, duplicate.Content = "other", "Should not overwrite"
	denial, err = AddGroupMemory(ctx, scope, duplicate, nil)
	require.NoError(t, err)
	require.NotEmpty(t, denial)
	denial, err = AddGroupMemory(ctx, otherScope, duplicate, nil)
	require.NoError(t, err)
	require.Empty(t, denial, "the same ID can be used in a different group")

	replacement := "Updated hours"
	for _, miss := range []struct {
		name                    string
		scope                   AgentV3Scope
		category, directory, id string
	}{
		{"category", scope, duplicate.Category, item.Directory, item.ID},
		{"directory", scope, item.Category, "cafeteria", item.ID},
		{"ID", scope, item.Category, item.Directory, "missing"},
		{"group", otherScope, item.Category, item.Directory, item.ID},
	} {
		t.Run(miss.name, func(t *testing.T) {
			read, err := ReadGroupMemory(ctx, miss.scope, miss.category, miss.directory, miss.id)
			require.NoError(t, err)
			require.Nil(t, read)
			denial, err := ChangeGroupMemory(ctx, miss.scope, miss.category, miss.directory, miss.id, &replacement, nil)
			require.NoError(t, err)
			require.NotEmpty(t, denial)
		})
	}
	read, err := ReadGroupMemory(ctx, otherScope, duplicate.Category, duplicate.Directory, duplicate.ID)
	require.NoError(t, err)
	require.NotNil(t, read)
	require.Equal(t, duplicate.Content, read.Content)
	original, err := ReadGroupMemory(ctx, scope, item.Category, item.Directory, item.ID)
	require.NoError(t, err)
	require.NotNil(t, original)
	require.Equal(t, item.Content, original.Content)
	require.Equal(t, item.CreatedBy, original.CreatedBy)

	const forbidden = "only the creator may modify this memory"
	editorCheck := func(editor int64) func([]AgentV3MemoryItem) string {
		return func(items []AgentV3MemoryItem) string {
			for _, current := range items {
				if current.ID == item.ID && current.CreatedBy == editor {
					return ""
				}
			}
			return forbidden
		}
	}
	for _, body := range []*string{&replacement, nil} {
		denial, err := ChangeGroupMemory(ctx, scope, item.Category, item.Directory, item.ID, body, editorCheck(8))
		require.NoError(t, err)
		require.Equal(t, forbidden, denial)
	}
	read, err = ReadGroupMemory(ctx, scope, item.Category, item.Directory, item.ID)
	require.NoError(t, err)
	require.Equal(t, original, read)

	denial, err = ChangeGroupMemory(ctx, scope, item.Category, item.Directory, item.ID, &replacement, editorCheck(7))
	require.NoError(t, err)
	require.Empty(t, denial)
	read, err = ReadGroupMemory(ctx, scope, item.Category, item.Directory, item.ID)
	require.NoError(t, err)
	updated := *original
	updated.Content = replacement
	require.Equal(t, &updated, read)
}

func TestGroupMemoryRechecksCapacityAfterConcurrentChange(t *testing.T) {
	for _, pendingAdd := range []bool{true, false} {
		name := "edit races with add"
		if pendingAdd {
			name = "add races with edit"
		}
		t.Run(name, func(t *testing.T) {
			setupAgentV3Redis(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			scope := AgentV3Scope{Bot: "bot", Platform: "tg", ChatID: -100}
			base := AgentV3MemoryItem{ID: "base", Category: "campus", Directory: "library", Description: "Library", Content: "x"}
			extra := AgentV3MemoryItem{ID: "extra", Category: "campus", Directory: "access", Description: "Access", Content: "y"}
			denial, err := AddGroupMemory(ctx, scope, base, nil)
			require.NoError(t, err)
			require.Empty(t, denial)

			longer := "xx"
			add := func(check func([]AgentV3MemoryItem) string) (string, error) {
				return AddGroupMemory(ctx, scope, extra, check)
			}
			edit := func(check func([]AgentV3MemoryItem) string) (string, error) {
				return ChangeGroupMemory(ctx, scope, base.Category, base.Directory, base.ID, &longer, check)
			}
			pending, concurrent := edit, add
			if pendingAdd {
				pending, concurrent = add, edit
			}

			const full = "group memory capacity exceeded"
			checked, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var pendingDenial string
			var pendingErr error
			go func() {
				defer close(done)
				firstCheck := true
				pendingDenial, pendingErr = pending(func(items []AgentV3MemoryItem) string {
					used := 0
					for _, current := range items {
						used += len(current.Content)
					}
					if used+1 > 2 {
						return full
					}
					if firstCheck {
						firstCheck = false
						close(checked)
						select {
						case <-release:
						case <-ctx.Done():
							return ctx.Err().Error()
						}
					}
					return ""
				})
			}()
			t.Cleanup(func() { <-done })
			select {
			case <-checked:
			case <-done:
				t.Fatalf("mutation returned before entering its capacity check: denial %q, error %v", pendingDenial, pendingErr)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			denial, err = concurrent(nil)
			close(release)
			require.NoError(t, err)
			require.Empty(t, denial)
			<-done
			require.NoError(t, pendingErr)
			require.Equal(t, full, pendingDenial)

			readBase, err := ReadGroupMemory(ctx, scope, base.Category, base.Directory, base.ID)
			require.NoError(t, err)
			require.NotNil(t, readBase)
			readExtra, err := ReadGroupMemory(ctx, scope, extra.Category, extra.Directory, extra.ID)
			require.NoError(t, err)
			if pendingAdd {
				require.Equal(t, longer, readBase.Content)
				require.Nil(t, readExtra)
			} else {
				require.Equal(t, base.Content, readBase.Content)
				require.NotNil(t, readExtra)
				require.Equal(t, extra.Content, readExtra.Content)
			}
		})
	}
}
