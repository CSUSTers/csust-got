//go:build !386 && !arm

package chatv2

import (
	"bytes"
	"encoding/json"
	"math"
)

type searXNGJSONFields struct {
	object map[string]json.RawMessage
	err    error
}

func decodeSearXNGSearchResults(body []byte, settings searXNGSearchSettings) ([]searXNGResult, error) {
	root, err := decodeSearXNGObject(body)
	if err != nil {
		return nil, err
	}
	raw, ok := root["results"]
	if !ok {
		return nil, errSearXNGMissingResults
	}
	items, err := decodeSearXNGRawArray(raw)
	if err != nil {
		return nil, err
	}
	results := make([]searXNGResult, 0, min(len(items), settings.numResults))
	for _, raw := range items {
		item, err := decodeSearXNGObject(raw)
		if err != nil {
			return nil, err
		}
		result, err := decodeSearXNGSearchResult(item)
		if err != nil {
			return nil, err
		}
		if settings.minScore != nil && (result.score == nil || *result.score < *settings.minScore) {
			continue
		}
		results = append(results, result)
		if len(results) == settings.numResults {
			break
		}
	}
	return results, nil
}
func decodeSearXNGSearchResult(item map[string]json.RawMessage) (searXNGResult, error) {
	fields := searXNGJSONFields{object: item}
	result := searXNGResult{title: fields.string("title", false), url: fields.string("url", true), summary: fields.string("content", false), engine: fields.string("engine", false), engines: fields.stringSlice("engines", false), category: fields.string("category", false), publishedDate: fields.string("publishedDate", false), score: fields.finiteFloat("score")}
	if fields.err != nil || !validSearXNGResultURL(result.url) {
		return searXNGResult{}, errSearXNGInvalidResult
	}
	return result, nil
}
func decodeSearXNGSuggestions(body []byte) ([]string, error) {
	items, err := decodeSearXNGRawArray(body)
	if err != nil {
		return nil, err
	}
	if strings, ok := decodeSearXNGStrings(items); ok {
		return strings, nil
	}
	if len(items) != 2 {
		return nil, errSearXNGInvalidSuggestionsTuple
	}
	if _, ok := decodeSearXNGJSONString(items[0]); !ok {
		return nil, errSearXNGInvalidSuggestionsQuery
	}
	suggestions, err := decodeSearXNGRawArray(items[1])
	if err != nil {
		return nil, err
	}
	if strings, ok := decodeSearXNGStrings(suggestions); ok {
		return strings, nil
	}
	return nil, errSearXNGInvalidSuggestions
}
func decodeSearXNGInstanceInfo(body []byte, includeEngines bool) (searXNGInstanceInfo, error) {
	root, err := decodeSearXNGObject(body)
	if err != nil {
		return searXNGInstanceInfo{}, err
	}
	fields := searXNGJSONFields{object: root}
	categories := fields.stringSlice("categories", false)
	_, categoriesPresent := root["categories"]
	info := searXNGInstanceInfo{instanceName: fields.string("instance_name", false), defaultLocale: fields.string("default_locale", false), safeSearch: fields.safeSearch(), categories: categories, categoriesPresent: categoriesPresent}
	if fields.err != nil {
		return searXNGInstanceInfo{}, fields.err
	}
	if !includeEngines {
		return info, nil
	}
	raw, _ := fields.value("engines", true)
	if fields.err != nil {
		return searXNGInstanceInfo{}, fields.err
	}
	info.engines, err = decodeSearXNGInstanceEngines(raw)
	if err != nil {
		return searXNGInstanceInfo{}, err
	}
	return info, nil
}
func decodeSearXNGInstanceEngines(raw json.RawMessage) ([]searXNGInstanceEngine, error) {
	items, err := decodeSearXNGRawArray(raw)
	if err != nil {
		return nil, err
	}
	engines := make([]searXNGInstanceEngine, 0, len(items))
	for _, raw := range items {
		item, err := decodeSearXNGObject(raw)
		if err != nil {
			return nil, err
		}
		fields := searXNGJSONFields{object: item}
		engine := searXNGInstanceEngine{name: fields.string("name", true), shortcut: fields.string("shortcut", true), categories: fields.stringSlice("categories", true), enabled: fields.bool("enabled", true)}
		if fields.err != nil {
			return nil, fields.err
		}
		engines = append(engines, engine)
	}
	return engines, nil
}
func decodeSearXNGObject(raw []byte) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errSearXNGExpectedObject
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}
func decodeSearXNGRawArray(raw []byte) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, errSearXNGExpectedArray
	}
	var value []json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}
func decodeSearXNGStrings(items []json.RawMessage) ([]string, bool) {
	strings := make([]string, len(items))
	for index, item := range items {
		value, ok := decodeSearXNGJSONString(item)
		if !ok {
			return nil, false
		}
		strings[index] = value
	}
	return strings, true
}
func decodeSearXNGJSONString(raw json.RawMessage) (string, bool) {
	if isSearXNGJSONNull(raw) {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func isSearXNGJSONNull(value []byte) bool { return bytes.Equal(bytes.TrimSpace(value), []byte("null")) }
func (f *searXNGJSONFields) value(name string, required bool) (json.RawMessage, bool) {
	if f.err != nil {
		return nil, false
	}
	raw, ok := f.object[name]
	if !ok && required {
		f.err = errSearXNGMissingRequiredField
	}
	return raw, ok
}
func (f *searXNGJSONFields) string(name string, required bool) string {
	raw, ok := f.value(name, required)
	if !ok {
		return ""
	}
	value, valid := decodeSearXNGJSONString(raw)
	if !valid {
		f.err = errSearXNGInvalidString
	}
	return value
}
func (f *searXNGJSONFields) stringSlice(name string, required bool) []string {
	raw, ok := f.value(name, required)
	if !ok {
		return nil
	}
	items, err := decodeSearXNGRawArray(raw)
	if err != nil {
		f.err = err
		return nil
	}
	values, valid := decodeSearXNGStrings(items)
	if !valid {
		f.err = errSearXNGInvalidStringArray
	}
	return values
}
func (f *searXNGJSONFields) finiteFloat(name string) *float64 {
	raw, ok := f.value(name, false)
	if !ok {
		return nil
	}
	if isSearXNGJSONNull(raw) {
		f.err = errSearXNGInvalidFiniteFloat
		return nil
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		f.err = errSearXNGInvalidFiniteFloat
		return nil
	}
	return &value
}
func (f *searXNGJSONFields) safeSearch() *int {
	raw, ok := f.value("safe_search", false)
	if !ok {
		return nil
	}
	if isSearXNGJSONNull(raw) {
		f.err = errSearXNGInvalidSafeSearch
		return nil
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil || value < 0 || value > 2 {
		f.err = errSearXNGInvalidSafeSearch
		return nil
	}
	return &value
}
func (f *searXNGJSONFields) bool(name string, required bool) bool {
	raw, ok := f.value(name, required)
	if !ok || isSearXNGJSONNull(raw) {
		f.err = errSearXNGInvalidBoolean
		return false
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		f.err = err
	}
	return value
}
