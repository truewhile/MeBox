// 远程 Emby 聚合的 ID 伪装。
//
// MeBox 作为 Emby 联邦网关把多个远程 Emby 服务器的媒体库透明聚合到自身的
// Emby API 之下，远程条目完全不落库。为了把本地 ID 与多个远程服务器的 ID
// 隔离开，远程条目在返回给客户端之前统一被改写为：
//
//	embyremote~{accountID}~{remoteID}
//
// 客户端后续对图片 / 详情 / 播放 / 播放状态 的请求都会携带这个伪装 ID，
// 服务端据此解码出对应账号与原始 ID，直接向远程 Emby 转发。
package service

import (
	"strings"
)

// EmbyRemoteIDPrefix 远程条目伪装 ID 的前缀（本地 UUID 与 Emby ID 不会出现 "~"）。
const EmbyRemoteIDPrefix = "embyremote~"

// IsEmbyRemoteID 报告 id 是否是伪装过的远程 Emby 条目 ID。
func IsEmbyRemoteID(id string) bool {
	return strings.HasPrefix(id, EmbyRemoteIDPrefix)
}

// EncodeEmbyRemoteID 把 (账号 ID, 远程条目 ID) 伪装为对外暴露的 ID。
func EncodeEmbyRemoteID(accountID, remoteID string) string {
	return EmbyRemoteIDPrefix + accountID + "~" + remoteID
}

// DecodeEmbyRemoteID 拆分伪装 ID 为 (账号 ID, 远程原始 ID)。不是伪装 ID 时返回
// ok=false。远程 ID 本身允许包含 "~"（使用 SplitN 只切第一刀）。
func DecodeEmbyRemoteID(id string) (accountID, remoteID string, ok bool) {
	if !IsEmbyRemoteID(id) {
		return "", "", false
	}
	rest := strings.TrimPrefix(id, EmbyRemoteIDPrefix)
	parts := strings.SplitN(rest, "~", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// embyRemoteStringIDs 是条目 JSON 中需要伪装（编码）成远程 ID 的字符串字段。
// 图片 / 详情 / 播放请求都会以这些字段的值作为 ID 回指 MeBox。
var embyRemoteStringIDs = []string{
	"Id",
	"ParentId",
	"SeriesId",
	"SeasonId",
	"PrimaryImageItemId",
	"DisplayPreferencesId",
}

// RewriteEmbyRemoteIDs 在内存中把远程 Emby 返回的载荷里的所有条目 ID 替换为
// 伪装 ID（防止与本地、多远程冲突），嵌套 Items / Map 数组递归处理。
//
// MediaSources 里的 Id / MediaSourceId 保持不变：客户端只把它们作为查询
// 参数原样带回，转发时直接送回远程即可。播放 URL 的重写由服务层
// （rewriteEmbyRemotePlayURLs）按直连/代理模式处理。
func RewriteEmbyRemoteIDs(value any, accountID string) {
	switch typed := value.(type) {
	case map[string]any:
		rewriteEmbyRemoteIDsMap(typed, accountID)
	case []any:
		for _, item := range typed {
			RewriteEmbyRemoteIDs(item, accountID)
		}
	case []map[string]any:
		for _, item := range typed {
			rewriteEmbyRemoteIDsMap(item, accountID)
		}
	}
}

func rewriteEmbyRemoteIDsMap(m map[string]any, accountID string) {
	if m == nil {
		return
	}
	for _, key := range embyRemoteStringIDs {
		if raw, ok := m[key].(string); ok && raw != "" {
			m[key] = EncodeEmbyRemoteID(accountID, raw)
		}
	}
	if tags, ok := m["ImageTags"].(map[string]any); ok {
		for k, v := range tags {
			if s, isStr := v.(string); isStr && s != "" {
				tags[k] = EncodeEmbyRemoteID(accountID, s)
			}
		}
	}
	if tags, ok := m["ImageTags"].(map[string]string); ok {
		for k, v := range tags {
			if v != "" {
				tags[k] = EncodeEmbyRemoteID(accountID, v)
			}
		}
	}
	if tags, ok := m["BackdropImageTags"].([]any); ok {
		for i := range tags {
			if s, isStr := tags[i].(string); isStr && s != "" {
				tags[i] = EncodeEmbyRemoteID(accountID, s)
			}
		}
	}
	if items, ok := m["Items"]; ok {
		RewriteEmbyRemoteIDs(items, accountID)
	}
	// 人物条目同样以 Id 回指 /Items/{Id}/Images/...。若不递归重写，客户端会
	// 把远程演员 ID 当成本地 ID，头像最终只能命中占位图。
	if people, ok := m["People"]; ok {
		RewriteEmbyRemoteIDs(people, accountID)
	}
}
