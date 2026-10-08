package config

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/viper"
	"go.uber.org/zap"
)

// configSchema describes one root section: a mapstructure-tagged type, or the flat keys its viper.Get* reader consumes.
type configSchema struct {
	typ  reflect.Type
	keys []string
}

var specialListConfigKeys = []string{"enabled", "chats"}

// rootConfigSchema mirrors readConfig(); flat key lists must match the viper.Get* calls in the sibling config files.
var rootConfigSchema = map[string]configSchema{
	"debug":               {},
	"url":                 {},
	"token":               {},
	"proxy":               {},
	"listen":              {},
	"skip_duration":       {},
	"log_file_dir":        {},
	"sentence_delimiters": {},
	"log":                 {keys: []string{"max_size_mb", "max_backups", "max_age_days", "compress"}},
	"redis":               {keys: []string{"addr", "pass", "key_prefix"}},
	"restrict":            {keys: []string{"kill_duration", "fake_ban_max_add"}},
	"rate_limit":          {keys: []string{"max_token", "limit", "cost", "cost_sticker", "cost_command", "expire_time"}},
	"message":             {keys: []string{"restrict_bot", "fake_ban_in_cd", "hitokoto_not_found", "no_sleep", "boot_failed", "welcome"}},
	"black_list":          {keys: specialListConfigKeys},
	"white_list":          {keys: specialListConfigKeys},
	"meili":               {keys: []string{"enabled", "address", "api_key", "index_prefix"}},
	"mc":                  {keys: []string{"mc2dead", "sacrifices", "odds", "timeout"}},
	"debugopt":            {keys: []string{"show_this"}},
	"get_voice":           {typ: reflect.TypeFor[GetVoiceConfig]()},
	"agents":              {typ: reflect.TypeFor[AgentV3Configs]()},
	"agent_v3":            {typ: reflect.TypeFor[AgentV3Config]()},
}

// UnknownConfigKeys lists loaded keys (config.yaml, custom.yaml) that no config field or reader accepts.
func UnknownConfigKeys() []string {
	var keys []string
	for name, raw := range viper.AllSettings() {
		schema, known := rootConfigSchema[strings.ToLower(name)]
		if !known {
			keys = append(keys, name)
			continue
		}
		keys = append(keys, schema.unknownKeys(raw, name)...)
	}
	sort.Strings(keys)
	return keys
}

func (s configSchema) unknownKeys(raw any, path string) []string {
	if s.typ != nil {
		return unknownConfigKeys(raw, s.typ, path)
	}
	if len(s.keys) == 0 {
		return nil
	}
	values, ok := agentV3SessionConfigMap(raw)
	if !ok {
		return nil
	}
	var keys []string
	for name := range values {
		if !slices.Contains(s.keys, name) {
			keys = append(keys, path+"."+name)
		}
	}
	return keys
}

// UnimplementedConfigKeys lists configured keys that are decoded but not applied anywhere.
func UnimplementedConfigKeys(agents *AgentV3Configs, v3 *AgentV3Config) []string {
	var keys []string
	if agents != nil {
		for i, agent := range *agents {
			if agent == nil {
				continue
			}
			prefix := fmt.Sprintf("agents[%d]", i)
			keys = append(keys, unimplementedModelKeys(agent.Model, prefix+".model")...)
			if agent.Agent != nil {
				for j, sub := range agent.Agent.SubAgents {
					if sub != nil {
						keys = append(keys, unimplementedModelKeys(sub.Model, fmt.Sprintf("%s.agent.subagents[%d].model", prefix, j))...)
					}
				}
			}
		}
	}
	if v3 != nil {
		keys = append(keys, unimplementedModelKeys(v3.Model, "agent_v3.model")...)
		if v3.ContextCache.PromptCacheRetention != "" {
			keys = append(keys, "agent_v3.context_cache.prompt_cache_retention")
		}
	}
	return keys
}

func unimplementedModelKeys(model *Model, prefix string) []string {
	if model == nil {
		return nil
	}
	var keys []string
	if model.Proxy != "" {
		keys = append(keys, prefix+".proxy")
	}
	if model.PromptLimit != 0 {
		keys = append(keys, prefix+".prompt_limit")
	}
	return keys
}

func warnConfigHygiene() {
	for _, key := range UnknownConfigKeys() {
		zap.L().Warn("unknown config key", zap.String("path", key))
	}
	for _, key := range UnimplementedConfigKeys(BotConfig.Agents, BotConfig.AgentV3) {
		zap.L().Warn("config key is read but not implemented", zap.String("path", key))
	}
}

func unknownConfigKeys(raw any, typ reflect.Type, path string) []string {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		return unknownStructKeys(raw, typ, path)
	case reflect.Slice, reflect.Array:
		if !configWalkable(typ.Elem()) {
			return nil
		}
		items, ok := configList(raw)
		if !ok {
			return nil
		}
		var keys []string
		for i, item := range items {
			keys = append(keys, unknownConfigKeys(item, typ.Elem(), fmt.Sprintf("%s[%d]", path, i))...)
		}
		return keys
	case reflect.Map:
		if !configWalkable(typ.Elem()) {
			return nil
		}
		fields, ok := agentV3SessionConfigMap(raw)
		if !ok {
			return nil
		}
		var keys []string
		for name, value := range fields {
			keys = append(keys, unknownConfigKeys(value, typ.Elem(), path+"."+name)...)
		}
		return keys
	default:
		return nil
	}
}

func unknownStructKeys(raw any, typ reflect.Type, path string) []string {
	fields := configStructFields(typ)
	if len(fields) == 0 {
		return nil
	}
	values, ok := agentV3SessionConfigMap(raw)
	if !ok {
		return nil
	}
	var keys []string
	for name, value := range values {
		field, known := fields[name]
		if !known {
			keys = append(keys, path+"."+name)
			continue
		}
		keys = append(keys, unknownConfigKeys(value, field, path+"."+name)...)
	}
	return keys
}

func configStructFields(typ reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type, typ.NumField())
	for i := range typ.NumField() {
		field := typ.Field(i)
		tag, _, _ := strings.Cut(field.Tag.Get("mapstructure"), ",")
		if tag == "" || tag == "-" {
			continue
		}
		fields[strings.ToLower(tag)] = field.Type
	}
	return fields
}

func configWalkable(typ reflect.Type) bool {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		return len(configStructFields(typ)) > 0
	case reflect.Slice, reflect.Array, reflect.Map:
		return configWalkable(typ.Elem())
	default:
		return false
	}
}

func configList(raw any) ([]any, bool) {
	v := reflect.ValueOf(raw)
	if !v.IsValid() || (v.Kind() != reflect.Slice && v.Kind() != reflect.Array) {
		return nil, false
	}
	items := make([]any, 0, v.Len())
	for i := range v.Len() {
		items = append(items, v.Index(i).Interface())
	}
	return items, true
}
