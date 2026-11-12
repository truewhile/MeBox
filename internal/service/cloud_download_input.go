// 云下载输入解析：把用户粘贴的多行文本拆成可提交的链接，并生成轮询匹配键。
//
// 支持的来源（与 115 /open/offline/add_task_urls 能力一致）：
//   - 磁力：magnet:?xt=urn:btih:<hex40 或 base32>
//   - 电驴：ed2k://|file|...
//   - HTTP(S) / FTP 直链
//
// 只做「字符串层面」的识别，绝不主动请求这些 URL（无 SSRF 面）。
package service

import (
	"net/url"
	"strings"
)

// cloudDownloadParsedLink 是一条解析后的待提交链接。
type cloudDownloadParsedLink struct {
	SourceType string // model.CloudDownloadSource*
	URL        string // 原始（trim 后）链接
	Key        string // 去重与轮询匹配键：BT 类为大写 info_hash，其余为链接本身
	InfoHash   string // 仅 BT 类有值
}

// cloudDownloadSkipped 是一条被跳过的原始输入。
type cloudDownloadSkipped struct {
	Raw    string `json:"raw"`
	Reason string `json:"reason"`
}

// parseCloudDownloadLinks 解析多行输入。
// 返回去重后的有效链接与被跳过的行（用于前端逐条反馈，而不是笼统报错）。
func parseCloudDownloadLinks(raw []string) ([]cloudDownloadParsedLink, []cloudDownloadSkipped) {
	out := make([]cloudDownloadParsedLink, 0, len(raw))
	skipped := make([]cloudDownloadSkipped, 0)
	seen := make(map[string]bool, len(raw))

	for _, line := range raw {
		// 允许多行文本整体粘贴：按换行再拆一次。
		for _, part := range strings.Split(line, "\n") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			link, reason := parseCloudDownloadLink(part)
			if reason != "" {
				skipped = append(skipped, cloudDownloadSkipped{Raw: part, Reason: reason})
				continue
			}
			if seen[link.Key] {
				skipped = append(skipped, cloudDownloadSkipped{Raw: part, Reason: "重复链接（本批已存在）"})
				continue
			}
			seen[link.Key] = true
			out = append(out, link)
		}
	}
	return out, skipped
}

// parseCloudDownloadLink 识别单条链接；reason 非空表示不支持。
func parseCloudDownloadLink(raw string) (cloudDownloadParsedLink, string) {
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "magnet:"):
		hash := magnetInfoHash(raw)
		if hash == "" {
			return cloudDownloadParsedLink{}, "磁力链接缺少有效的 btih 哈希"
		}
		return cloudDownloadParsedLink{
			SourceType: "magnet",
			URL:        raw,
			Key:        hash,
			InfoHash:   hash,
		}, ""
	case strings.HasPrefix(lower, "ed2k://"):
		return cloudDownloadParsedLink{
			SourceType: "ed2k",
			URL:        raw,
			Key:        raw,
		}, ""
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		return cloudDownloadParsedLink{
			SourceType: "http",
			URL:        raw,
			Key:        raw,
		}, ""
	case strings.HasPrefix(lower, "ftp://"):
		return cloudDownloadParsedLink{
			SourceType: "ftp",
			URL:        raw,
			Key:        raw,
		}, ""
	default:
		return cloudDownloadParsedLink{}, "不支持的链接类型（仅支持磁力 / ed2k / http(s) / ftp）"
	}
}

// magnetInfoHash 从磁力链接中提取大写化的 btih 哈希。
// 兼容 hex（40 字符）与 base32（32 字符）两种写法；无法识别时返回空串。
func magnetInfoHash(link string) string {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return ""
	}
	for _, xt := range u.Query()["xt"] {
		const prefix = "urn:btih:"
		if len(xt) <= len(prefix) || !strings.EqualFold(xt[:len(prefix)], prefix) {
			continue
		}
		hash := strings.TrimSpace(xt[len(prefix):])
		// 去掉可能存在的 &dn= 之类残留（少数客户端拼接不规范）。
		if idx := strings.IndexAny(hash, "&?#"); idx >= 0 {
			hash = hash[:idx]
		}
		if len(hash) != 40 && len(hash) != 32 {
			continue
		}
		return strings.ToUpper(hash)
	}
	return ""
}
