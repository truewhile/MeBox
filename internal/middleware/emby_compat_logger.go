package middleware

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// EmbyCompatLogger 将 Emby/Jellyfin 兼容面的请求独立记录，便于针对具体
// 客户端、设备和未实现接口排查兼容问题。
//
// isEmbyPath 由 handler 层提供，用于识别 /emby/* 以及无前缀根路径形式的
// Emby API。除路径特征外，带 X-Emby-* / X-MediaBrowser-* 等客户端凭据的
// 请求也会被识别，因此尚未实现且没有已知路径前缀的新接口也能被记录。
func EmbyCompatLogger(log *zap.Logger, isEmbyPath func(string) bool) gin.HandlerFunc {
	if log == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		if c == nil || c.Request == nil {
			return
		}

		requestPath := c.Request.URL.Path
		pathMatched := isEmbyPath != nil && isEmbyPath(requestPath)
		hasSignature := hasEmbyClientSignature(c)
		if !pathMatched && !hasSignature {
			c.Next()
			return
		}

		start := time.Now()
		c.Next()

		status := c.Writer.Status()
		route := c.FullPath()
		// 根路径 Emby 命名空间很宽，可能与 SPA 路由重名。没有客户端标识且
		// 最终由 SPA 返回 200 时不应污染兼容日志；服务端路由或 404 仍记录。
		if !hasSignature && !hasExplicitEmbyPrefix(requestPath) && strings.TrimSpace(route) == "" && status < http.StatusBadRequest {
			return
		}
		unimplemented := isUnimplementedEmbyRequest(c, route, status, hasSignature)
		fields := embyCompatLogFields(c, requestPath, status, route, time.Since(start), unimplemented)

		switch {
		case unimplemented:
			log.Warn("emby API not implemented", fields...)
		case status >= http.StatusBadRequest:
			log.Warn("emby request failed", fields...)
		default:
			log.Info("emby request", fields...)
		}
	}
}

func isUnimplementedEmbyRequest(c *gin.Context, route string, status int, hasSignature bool) bool {
	if strings.TrimSpace(route) != "" || c.Request.Method == http.MethodOptions {
		return false
	}
	if status == http.StatusNotFound {
		return true
	}
	// 无前端 API 前缀的未知 Emby 路径可能被 SPA 兜底为 200 HTML。
	// 已识别为 Emby 客户端的这类响应同样说明兼容接口尚未实现。
	return hasSignature && status >= http.StatusOK && status < http.StatusMultipleChoices &&
		strings.Contains(strings.ToLower(c.Writer.Header().Get("Content-Type")), "text/html")
}

func embyCompatLogFields(c *gin.Context, requestPath string, status int, route string, duration time.Duration, unimplemented bool) []zap.Field {
	client := embyCompatClientInfo(c)
	fields := []zap.Field{
		zap.String("method", c.Request.Method),
		zap.String("path", requestPath),
		zap.String("normalized_path", c.Request.URL.Path),
		zap.String("route", route),
		zap.Int("status", status),
		zap.Duration("duration", duration),
		zap.String("ip", c.ClientIP()),
		zap.Bool("unimplemented", unimplemented),
	}
	if queryKeys := sortedQueryKeys(c); len(queryKeys) > 0 {
		fields = append(fields, zap.Strings("query_keys", queryKeys))
	}
	if client.Client != "" {
		fields = append(fields, zap.String("client", client.Client))
	}
	if client.Device != "" {
		fields = append(fields, zap.String("device", client.Device))
	}
	if client.DeviceID != "" {
		fields = append(fields, zap.String("device_id", client.DeviceID))
	}
	if client.Version != "" {
		fields = append(fields, zap.String("client_version", client.Version))
	}
	if client.UserID != "" {
		fields = append(fields, zap.String("user_id", client.UserID))
	}
	if userAgent := strings.TrimSpace(c.GetHeader("User-Agent")); userAgent != "" {
		fields = append(fields, zap.String("user_agent", userAgent))
	}
	if contentType := strings.TrimSpace(c.GetHeader("Content-Type")); contentType != "" {
		fields = append(fields, zap.String("content_type", contentType))
	}
	if errs := c.Errors.Errors(); len(errs) > 0 {
		fields = append(fields, zap.Strings("errors", truncateStrings(errs, 8, 512)))
	}
	return fields
}

func hasEmbyClientSignature(c *gin.Context) bool {
	for _, name := range []string{
		"X-Emby-Token",
		"X-MediaBrowser-Token",
		"X-Emby-Authorization",
		"X-MediaBrowser-Authorization",
		"X-Emby-Client",
		"X-MediaBrowser-Client",
		"X-Emby-Device-Id",
		"X-Emby-DeviceId",
		"X-Emby-Device-Name",
		"X-Emby-Version",
		"X-Emby-UserId",
		"X-MediaBrowser-Device-Id",
		"X-MediaBrowser-DeviceId",
		"X-MediaBrowser-Device-Name",
		"X-MediaBrowser-Version",
		"X-MediaBrowser-UserId",
	} {
		if strings.TrimSpace(c.GetHeader(name)) != "" {
			return true
		}
	}

	auth := strings.ToLower(strings.TrimSpace(c.GetHeader("Authorization")))
	if strings.HasPrefix(auth, "emby ") || strings.HasPrefix(auth, "mediabrowser ") {
		return true
	}

	for _, key := range []string{"X-Emby-Token", "X-MediaBrowser-Token"} {
		if strings.TrimSpace(c.Query(key)) != "" {
			return true
		}
	}

	ua := strings.ToLower(strings.TrimSpace(c.GetHeader("User-Agent")))
	for _, marker := range []string{
		"emby", "jellyfin", "infuse", "senplayer", "fileball", "vidhub", "hills", "rodelplayer",
	} {
		if strings.Contains(ua, marker) {
			return true
		}
	}
	return false
}

type embyCompatClient struct {
	Client   string
	Device   string
	DeviceID string
	Version  string
	UserID   string
}

func embyCompatClientInfo(c *gin.Context) embyCompatClient {
	auth := parseEmbyCompatAuthorization(firstNonEmptyString(
		c.GetHeader("X-Emby-Authorization"),
		c.GetHeader("X-MediaBrowser-Authorization"),
		c.GetHeader("Authorization"),
	))
	return embyCompatClient{
		Client: firstNonEmptyString(
			c.GetHeader("X-Emby-Client"),
			c.GetHeader("X-MediaBrowser-Client"),
			c.Query("Client"),
			c.Query("client"),
			c.Query("X-Emby-Client"),
			auth["client"],
		),
		Device: firstNonEmptyString(
			c.GetHeader("X-Emby-Device-Name"),
			c.GetHeader("X-MediaBrowser-Device-Name"),
			c.Query("Device"),
			c.Query("DeviceName"),
			c.Query("device"),
			c.Query("deviceName"),
			auth["device"],
		),
		DeviceID: firstNonEmptyString(
			c.GetHeader("X-Emby-Device-Id"),
			c.GetHeader("X-Emby-DeviceId"),
			c.GetHeader("X-MediaBrowser-Device-Id"),
			c.GetHeader("X-MediaBrowser-DeviceId"),
			c.Query("DeviceId"),
			c.Query("DeviceID"),
			c.Query("deviceId"),
			c.Query("deviceID"),
			auth["deviceid"],
		),
		Version: firstNonEmptyString(
			c.GetHeader("X-Emby-Version"),
			c.GetHeader("X-MediaBrowser-Version"),
			c.Query("Version"),
			c.Query("version"),
			auth["version"],
		),
		UserID: firstNonEmptyString(
			c.Query("UserId"),
			c.Query("userId"),
			auth["userid"],
		),
	}
}

func parseEmbyCompatAuthorization(raw string) map[string]string {
	out := map[string]string{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return out
	}
	if len(raw) >= len("MediaBrowser ") && strings.EqualFold(raw[:len("MediaBrowser ")], "MediaBrowser ") {
		raw = raw[len("MediaBrowser "):]
	} else if len(raw) >= len("Emby ") && strings.EqualFold(raw[:len("Emby ")], "Emby ") {
		raw = raw[len("Emby "):]
	}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.Trim(strings.TrimSpace(value), `"`)
		if key != "" && value != "" {
			out[key] = value
		}
	}
	return out
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func sortedQueryKeys(c *gin.Context) []string {
	query := c.Request.URL.Query()
	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func truncateStrings(values []string, maxItems, maxLen int) []string {
	if len(values) > maxItems {
		values = values[:maxItems]
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if len(value) > maxLen {
			value = value[:maxLen] + "..."
		}
		out = append(out, value)
	}
	return out
}

func hasExplicitEmbyPrefix(path string) bool {
	lower := strings.ToLower(strings.TrimSpace(path))
	return lower == "/emby" || strings.HasPrefix(lower, "/emby/")
}
