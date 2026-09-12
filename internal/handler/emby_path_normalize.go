package handler

import (
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
)

var (
	multipleSlashesRE = regexp.MustCompile(`/{2,}`)
)

// embyStaticSegments 包含 Emby API 中已知的保留静态路由分段（全部小写），
// 用于在遇到客户端混合大小写（如 /System/info, /items/:id/playbackInfo）时，
// 将静态段规范化为对应的小写形式，而保留动态参数段（:id, :userId 等）的原大小写。
var embyStaticSegments = map[string]struct{}{
	"system":                 {},
	"info":                   {},
	"public":                 {},
	"endpoint":               {},
	"configuration":          {},
	"ping":                   {},
	"users":                  {},
	"useritems":              {},
	"me":                     {},
	"authenticatebyname":     {},
	"items":                  {},
	"counts":                 {},
	"latest":                 {},
	"resume":                 {},
	"playbackinfo":           {},
	"shows":                  {},
	"seasons":                {},
	"episodes":               {},
	"nextup":                 {},
	"upcoming":               {},
	"similar":                {},
	"thumbnailset":           {},
	"thememedia":             {},
	"specialfeatures":        {},
	"intros":                 {},
	"videos":                 {},
	"stream":                 {},
	"subtitles":              {},
	"master.m3u8":            {},
	"main.m3u8":              {},
	"sessions":               {},
	"playing":                {},
	"progress":               {},
	"stopped":                {},
	"capabilities":           {},
	"full":                   {},
	"logout":                 {},
	"views":                  {},
	"library":                {},
	"mediafolders":           {},
	"virtualfolders":         {},
	"selectablemediafolders": {},
	"branding":               {},
	"css":                    {},
	"localization":           {},
	"options":                {},
	"cultures":               {},
	"customcssjs":            {},
	"scripts":                {},
	"displaypreferences":     {},
	"quickconnect":           {},
	"enabled":                {},
	"startup":                {},
	"complete":               {},
	"favoriteitems":          {},
	"playeditems":            {},
	"images":                 {},
	"primary":                {},
	"backdrop":               {},
	"banner":                 {},
	"thumb":                  {},
	"logo":                   {},
	"serverdomains":          {},
	"ext":                    {},
	"danmu":                  {},
	"raw":                    {},
	"mediasegments":          {},
	"artists":                {},
	"persons":                {},
	"genres":                 {},
	"embywebsocket":          {},
}

var embyRootSegments = map[string]struct{}{
	"albums":             {},
	"artists":            {},
	"audio":              {},
	"audiocodecs":        {},
	"auth":               {},
	"branding":           {},
	"channels":           {},
	"collections":        {},
	"connect":            {},
	"containers":         {},
	"devices":            {},
	"displaypreferences": {},
	"dlna":               {},
	"encoding":           {},
	"environment":        {},
	"gamegenres":         {},
	"games":              {},
	"genres":             {},
	"images":             {},
	"items":              {},
	"libraries":          {},
	"library":            {},
	"livestreams":        {},
	"livetv":             {},
	"localization":       {},
	"movies":             {},
	"musicgenres":        {},
	"news":               {},
	"notification":       {},
	"notifications":      {},
	"officialratings":    {},
	"packages":           {},
	"persons":            {},
	"playback":           {},
	"playlists":          {},
	"plugins":            {},
	"providers":          {},
	"reports":            {},
	"scheduledtasks":     {},
	"search":             {},
	"sessions":           {},
	"shows":              {},
	"songs":              {},
	"studios":            {},
	"subtitlecodecs":     {},
	"sync":               {},
	"system":             {},
	"tags":               {},
	"trailers":           {},
	"user_usage_stats":   {},
	"users":              {},
	"videocodecs":        {},
	"videos":             {},
	"years":              {},
}

func isEmbyRootSegment(segment string) bool {
	if _, ok := embyRootSegments[segment]; ok {
		return true
	}
	_, ok := embyStaticSegments[segment]
	return ok
}

// IsEmbyPath 判断路径是否属于 Emby/Jellyfin 兼容面的命名空间。
// 除显式 /emby 前缀外，Emby 客户端也会直接请求客户端协议的根路径，
// 例如 /System/Info/Public、/Users/Public、/Sessions。
func IsEmbyPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	path = multipleSlashesRE.ReplaceAllString(path, "/")
	lower := strings.ToLower(path)
	if lower == "/emby" || strings.HasPrefix(lower, "/emby/") {
		return true
	}

	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) == 0 || segments[0] == "" {
		return false
	}
	return isEmbyRootSegment(strings.ToLower(segments[0]))
}

// NormalizeEmbyPath 规范化 Emby 请求路径：
// 1. 折叠重复斜杠（如 //emby/ -> /emby/）；
// 2. 折叠重复前缀（如 /emby/emby/System/Info -> /emby/System/Info）；
// 3. 将静态关键字段归一化为小写，同时保留动态 ID/参数的原有大小写。
func NormalizeEmbyPath(p string) (string, bool) {
	if p == "" {
		return "", false
	}
	original := p

	// 1. 折叠多余斜杠
	p = multipleSlashesRE.ReplaceAllString(p, "/")

	// 2. 折叠重复的 /emby 前缀
	for {
		lower := strings.ToLower(p)
		if strings.HasPrefix(lower, "/emby/emby/") {
			p = "/emby/" + p[len("/emby/emby/"):]
			continue
		}
		if lower == "/emby/emby" {
			p = "/emby"
			break
		}
		break
	}

	// 3. 分析是否具有 Emby 路由特征
	hasEmbyPrefix := false
	workPath := p
	if strings.HasPrefix(strings.ToLower(workPath), "/emby/") {
		hasEmbyPrefix = true
		workPath = workPath[len("/emby"):]
	} else if strings.EqualFold(workPath, "/emby") {
		return "/emby", original != "/emby"
	}

	segments := strings.Split(strings.Trim(workPath, "/"), "/")
	if len(segments) == 0 || segments[0] == "" {
		return p, p != original
	}

	// 检查第一段是否为 Emby 根路由关键字
	firstLower := strings.ToLower(segments[0])
	if !isEmbyRootSegment(firstLower) && firstLower != "api" {
		// 不是 Emby 相关路径，保持原样
		return original, false
	}

	// 4. 将已知静态段转为小写，动态段保留原样
	for i, seg := range segments {
		segLower := strings.ToLower(seg)
		if _, isStatic := embyStaticSegments[segLower]; isStatic {
			if seg != segLower {
				segments[i] = segLower
			}
		}
	}

	var builder strings.Builder
	if hasEmbyPrefix {
		builder.WriteString("/emby")
	}
	for _, seg := range segments {
		builder.WriteString("/")
		builder.WriteString(seg)
	}
	if strings.HasSuffix(original, "/") && !strings.HasSuffix(builder.String(), "/") {
		builder.WriteString("/")
	}

	normalized := builder.String()
	return normalized, normalized != original
}

const embyNormalizedCtxKey = "emby_normalized_path"

// TryHandleEmbyNormalizedRoute 尝试在 404 NoRoute 阶段对 Emby 路径做前缀与大小写纠偏并重定向分发。
// 若成功分发并处理，返回 true；否则返回 false。
func TryHandleEmbyNormalizedRoute(c *gin.Context, r *gin.Engine) bool {
	if c == nil || r == nil {
		return false
	}
	if c.GetBool(embyNormalizedCtxKey) {
		return false
	}
	normalized, changed := NormalizeEmbyPath(c.Request.URL.Path)
	if !changed {
		return false
	}

	c.Set(embyNormalizedCtxKey, true)
	c.Request.URL.Path = normalized

	// 重置 context 状态并由 engine 重新查找路由树
	c.Params = nil
	c.Writer.Header().Del("Content-Type")
	r.HandleContext(c)
	return true
}
