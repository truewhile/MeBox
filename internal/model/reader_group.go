package model

import (
	"encoding/json"
	"strings"
)

// 书架分组（对应 legado 的 BookGroup）。
//
// 与影视模块的媒体库标签（LibraryTagSet）同构：组名 → 成员 ID 列表，一个成员只归一个组。
// 区别只是归属对象从媒体库换成了书籍，因此前端可以照搬那套标签栏 / 管理弹窗的交互。

const (
	// MaxBookGroups 是单个用户可创建的分组数量上限，避免恶意写入过大的 JSON。
	MaxBookGroups = 50
	// MaxBookGroupNameLen 是单个分组名的最大字符长度（按 rune 计数）。
	MaxBookGroupNameLen = 24
)

// BookGroupSet 是一个书架分组：组名 + 组内书籍 ID（顺序即组内展示顺序）。
type BookGroupSet struct {
	Name    string   `json:"name"`
	BookIDs []string `json:"book_ids"`
}

// ReaderBookGroups 是某个用户的书架分组集合，每个用户一条记录。
//
// 分组是「整份替换」语义（前端一次 PUT 提交完整分组列表），所以直接存成一份 JSON；
// 与阅读器偏好（ReaderProfile）分表存放，两条写入路径互不干扰：
// 先建分组、后调偏好时不会因为整行 Save 把对方的列写成零值。
type ReaderBookGroups struct {
	Base
	UserID string `gorm:"type:varchar(36);uniqueIndex" json:"user_id"`
	// Groups 是 []BookGroupSet 的 JSON 文本；空字符串表示没有分组。
	Groups string `gorm:"type:text" json:"-"`
}

// DecodeBookGroups 解析 Groups 字段，忽略损坏的数据。
func (r *ReaderBookGroups) DecodeBookGroups() []BookGroupSet {
	if r == nil || strings.TrimSpace(r.Groups) == "" {
		return nil
	}
	var groups []BookGroupSet
	if err := json.Unmarshal([]byte(r.Groups), &groups); err != nil {
		return nil
	}
	return NormalizeBookGroups(groups)
}

// EncodeBookGroups 把分组集合序列化为可写入 Groups 字段的 JSON 文本。
// 空集合序列化为空字符串，便于用零值表达「没有分组」。
func EncodeBookGroups(groups []BookGroupSet) (string, error) {
	if len(groups) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(groups)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// NormalizeBookGroups 清洗分组集合：去掉空名分组、合并重名分组、去掉组内重复的
// 书籍 ID，并保持传入顺序。不在这里做「一本书只归一个组」的收敛——那需要知道
// 书籍是否存在，属于服务层（SetBookGroups）的职责。
func NormalizeBookGroups(groups []BookGroupSet) []BookGroupSet {
	if len(groups) == 0 {
		return nil
	}
	out := make([]BookGroupSet, 0, len(groups))
	indexByName := make(map[string]int, len(groups))
	for _, group := range groups {
		name := TruncateBookGroupName(group.Name)
		if name == "" {
			continue
		}
		if len(out) >= MaxBookGroups {
			break
		}
		key := strings.ToLower(name)
		pos, exists := indexByName[key]
		if !exists {
			out = append(out, BookGroupSet{Name: name, BookIDs: []string{}})
			pos = len(out) - 1
			indexByName[key] = pos
		}
		seen := make(map[string]struct{}, len(out[pos].BookIDs))
		for _, id := range out[pos].BookIDs {
			seen[id] = struct{}{}
		}
		for _, id := range group.BookIDs {
			trimmed := strings.TrimSpace(id)
			if trimmed == "" {
				continue
			}
			if _, ok := seen[trimmed]; ok {
				continue
			}
			seen[trimmed] = struct{}{}
			out[pos].BookIDs = append(out[pos].BookIDs, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	for i := range out {
		if out[i].BookIDs == nil {
			out[i].BookIDs = []string{}
		}
	}
	return out
}

// TruncateBookGroupName 去掉首尾空白并按 rune 截断到长度上限。
func TruncateBookGroupName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	runes := []rune(name)
	if len(runes) > MaxBookGroupNameLen {
		runes = runes[:MaxBookGroupNameLen]
	}
	return strings.TrimSpace(string(runes))
}
