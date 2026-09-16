package model

import (
	"encoding/json"
	"strings"
)

// MaxLibraryTags 是单个用户可创建的标签数量上限，避免恶意写入过大的 JSON。
const MaxLibraryTags = 50

// MaxLibraryTagNameLen 是单个标签名的最大字符长度（按 rune 计数）。
const MaxLibraryTagNameLen = 24

// LibraryTagSet 是用户自定义的媒体库标签分组。
// LibraryIDs 保存该标签下媒体库的 ID（含远程 Emby 挂载库的 embyremote~ 形式），
// 顺序即媒体库在该标签内的展示顺序。
type LibraryTagSet struct {
	Name       string   `json:"name"`
	LibraryIDs []string `json:"library_ids"`
}

// DecodeLibraryTags 解析 LibraryTags 字段，忽略损坏的数据。
func (u *User) DecodeLibraryTags() []LibraryTagSet {
	if u == nil || strings.TrimSpace(u.LibraryTags) == "" {
		return nil
	}
	var tags []LibraryTagSet
	if err := json.Unmarshal([]byte(u.LibraryTags), &tags); err != nil {
		return nil
	}
	out := make([]LibraryTagSet, 0, len(tags))
	for _, tag := range NormalizeLibraryTags(tags) {
		out = append(out, tag)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// EncodeLibraryTags 把标签集合序列化为可写入 LibraryTags 字段的 JSON 字符串。
// 空集合序列化为空字符串，便于用零值表达"没有标签"。
func EncodeLibraryTags(tags []LibraryTagSet) (string, error) {
	if len(tags) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(tags)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// NormalizeLibraryTags 清洗标签集合：去空白、丢弃空名标签、去掉标签名与
// 标签内媒体库 ID 的重复项，并把标签名重复的项合并。保持传入顺序。
func NormalizeLibraryTags(tags []LibraryTagSet) []LibraryTagSet {
	if len(tags) == 0 {
		return nil
	}
	out := make([]LibraryTagSet, 0, len(tags))
	indexByName := make(map[string]int, len(tags))
	for _, tag := range tags {
		name := TruncateLibraryTagName(tag.Name)
		if name == "" {
			continue
		}
		if len(out) >= MaxLibraryTags {
			break
		}
		key := strings.ToLower(name)
		pos, exists := indexByName[key]
		if !exists {
			if len(out) >= MaxLibraryTags {
				break
			}
			out = append(out, LibraryTagSet{Name: name, LibraryIDs: []string{}})
			pos = len(out) - 1
			indexByName[key] = pos
		}
		seen := make(map[string]struct{}, len(out[pos].LibraryIDs))
		for _, id := range out[pos].LibraryIDs {
			seen[id] = struct{}{}
		}
		for _, id := range tag.LibraryIDs {
			trimmed := strings.TrimSpace(id)
			if trimmed == "" {
				continue
			}
			if _, ok := seen[trimmed]; ok {
				continue
			}
			seen[trimmed] = struct{}{}
			out[pos].LibraryIDs = append(out[pos].LibraryIDs, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	for i := range out {
		if out[i].LibraryIDs == nil {
			out[i].LibraryIDs = []string{}
		}
	}
	return out
}

// TruncateLibraryTagName 去掉首尾空白并按 rune 截断到长度上限。
func TruncateLibraryTagName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	runes := []rune(name)
	if len(runes) > MaxLibraryTagNameLen {
		runes = runes[:MaxLibraryTagNameLen]
	}
	return strings.TrimSpace(string(runes))
}
