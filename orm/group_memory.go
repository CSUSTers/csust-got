package orm

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/redis/go-redis/v9"
)

// GroupMemoryDirectory contains the recall index without any item content.
type GroupMemoryDirectory struct {
	Category    string   `json:"category"`
	Directory   string   `json:"directory"`
	Description string   `json:"description"`
	IDs         []string `json:"ids"`
}

const groupDirectoriesField = "directories"

func groupMemoryKey(scope AgentV3Scope) string {
	return agentV3BaseKey(scope) + ":group_catalog"
}

// GroupMemoryCatalog returns the directory index without loading entry bodies.
func GroupMemoryCatalog(ctx context.Context, scope AgentV3Scope) ([]GroupMemoryDirectory, error) {
	data, err := rc.HGet(ctx, groupMemoryKey(scope), groupDirectoriesField).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var catalog []GroupMemoryDirectory
	err = json.Unmarshal(data, &catalog)
	return catalog, err
}

// ReadGroupMemory returns nil when the ID does not belong to the specified directory.
func ReadGroupMemory(ctx context.Context, scope AgentV3Scope, category, directory, id string) (*AgentV3MemoryItem, error) {
	data, err := rc.HGet(ctx, groupMemoryKey(scope), "item:"+id).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var item AgentV3MemoryItem
	if err := json.Unmarshal(data, &item); err != nil {
		return nil, err
	}
	if item.Category != category || item.Directory != directory || item.ID != id {
		return nil, nil
	}
	return &item, nil
}

// AddGroupMemory applies check to the current chat entries in the insertion transaction.
func AddGroupMemory(ctx context.Context, scope AgentV3Scope, item AgentV3MemoryItem, check func([]AgentV3MemoryItem) string) (string, error) {
	if item.ID == "" || item.Category == "" || item.Directory == "" {
		return "请指定记忆 ID、分类和目录", nil
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}
	return writeGroupMemory(ctx, scope, item, true, nil, check)
}

// ChangeGroupMemory replaces the entry body, or deletes the entry if replacement is nil.
func ChangeGroupMemory(ctx context.Context, scope AgentV3Scope, category, directory, id string, replacement *string, check func([]AgentV3MemoryItem) string) (string, error) {
	target := AgentV3MemoryItem{ID: id, Category: category, Directory: directory}
	return writeGroupMemory(ctx, scope, target, false, replacement, check)
}

func writeGroupMemory(ctx context.Context, scope AgentV3Scope, target AgentV3MemoryItem, add bool, replacement *string, check func([]AgentV3MemoryItem) string) (string, error) {
	key := groupMemoryKey(scope)
	field := "item:" + target.ID
	var denial string
	err := agentV3WatchRetry(ctx, []string{key}, func(tx *redis.Tx) error {
		fields, err := tx.HGetAll(ctx, key).Result()
		if err != nil {
			return err
		}
		var dirs []GroupMemoryDirectory
		if data, ok := fields[groupDirectoriesField]; ok {
			if err := json.Unmarshal([]byte(data), &dirs); err != nil {
				return err
			}
		}
		dir := slices.IndexFunc(dirs, func(d GroupMemoryDirectory) bool {
			return d.Category == target.Category && d.Directory == target.Directory
		})
		item := target
		entry := -1
		data, exists := fields[field]
		if add {
			if exists {
				denial = "记忆 ID 已存在"
				return nil
			}
			if dir >= 0 {
				item.Description = dirs[dir].Description
			}
		} else {
			if dir >= 0 {
				entry = slices.Index(dirs[dir].IDs, target.ID)
			}
			if !exists || entry < 0 {
				denial = "该目录下未找到此记忆 ID"
				return nil
			}
			if err := json.Unmarshal([]byte(data), &item); err != nil {
				return err
			}
			if replacement != nil {
				item.Content = *replacement
			}
		}
		if check != nil {
			items := make([]AgentV3MemoryItem, 0, len(fields))
			for f, data := range fields {
				if f == groupDirectoriesField {
					continue
				}
				var existing AgentV3MemoryItem
				if err := json.Unmarshal([]byte(data), &existing); err != nil {
					return err
				}
				items = append(items, existing)
			}
			if denial = check(items); denial != "" {
				return nil
			}
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			if add || replacement != nil {
				encoded, err := json.Marshal(item)
				if err != nil {
					return err
				}
				pipe.HSet(ctx, key, field, encoded)
				if !add {
					return nil
				}
				if dir >= 0 {
					dirs[dir].IDs = append(dirs[dir].IDs, item.ID)
					slices.Sort(dirs[dir].IDs)
				} else {
					dirs = append(dirs, GroupMemoryDirectory{Category: item.Category, Directory: item.Directory, Description: item.Description, IDs: []string{item.ID}})
					slices.SortFunc(dirs, func(a, b GroupMemoryDirectory) int {
						return cmp.Or(cmp.Compare(a.Category, b.Category), cmp.Compare(a.Directory, b.Directory))
					})
				}
			} else {
				pipe.HDel(ctx, key, field)
				dirs[dir].IDs = slices.Delete(dirs[dir].IDs, entry, entry+1)
				if len(dirs[dir].IDs) == 0 {
					dirs = slices.Delete(dirs, dir, dir+1)
				}
			}
			if len(dirs) == 0 {
				pipe.HDel(ctx, key, groupDirectoriesField)
				return nil
			}
			encoded, err := json.Marshal(dirs)
			if err != nil {
				return err
			}
			pipe.HSet(ctx, key, groupDirectoriesField, encoded)
			return nil
		})
		return err
	})
	return denial, err
}
