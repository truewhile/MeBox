package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/truewhile/MeBox/internal/model"
)

// EmbyRemoteLine 表示同一 Emby 服务器的一条接入线路（内网/外网/CDN 等）。
type EmbyRemoteLine struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

func normalizeEmbyRemoteURL(raw string) string {
	u := strings.TrimRight(strings.TrimSpace(raw), "/")
	if u == "" {
		return ""
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return ""
	}
	return u
}

// NormalizeEmbyRemoteLines 去掉空 URL，并规范每条线路地址。
func NormalizeEmbyRemoteLines(lines []EmbyRemoteLine) []EmbyRemoteLine {
	out := make([]EmbyRemoteLine, 0, len(lines))
	for _, line := range lines {
		u := normalizeEmbyRemoteURL(line.URL)
		if u == "" {
			continue
		}
		out = append(out, EmbyRemoteLine{
			Name: strings.TrimSpace(line.Name),
			URL:  u,
		})
	}
	return out
}

// ParseEmbyRemoteLines 从账号配置 JSON 中解析线路列表。
// 兼容旧版单字段 url；urls 为 JSON 数组字符串。
func ParseEmbyRemoteLines(raw map[string]string) ([]EmbyRemoteLine, int, error) {
	active := 0
	if v := strings.TrimSpace(raw["active_line"]); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			active = n
		}
	}
	if urlsJSON := strings.TrimSpace(raw["urls"]); urlsJSON != "" {
		var lines []EmbyRemoteLine
		if err := json.Unmarshal([]byte(urlsJSON), &lines); err != nil {
			return nil, 0, fmt.Errorf("解析 Emby 线路列表: %w", err)
		}
		lines = NormalizeEmbyRemoteLines(lines)
		if len(lines) == 0 {
			return nil, 0, errors.New("至少配置一条 Emby 线路")
		}
		if active >= len(lines) {
			active = 0
		}
		return lines, active, nil
	}
	if u := normalizeEmbyRemoteURL(raw["url"]); u != "" {
		return []EmbyRemoteLine{{URL: u}}, 0, nil
	}
	return nil, 0, errors.New("缺少 Emby 地址")
}

// EncodeEmbyRemoteLines 序列化线路列表，并返回兼容旧版的 primary URL。
func EncodeEmbyRemoteLines(lines []EmbyRemoteLine) (urlsJSON string, primaryURL string, err error) {
	lines = NormalizeEmbyRemoteLines(lines)
	if len(lines) == 0 {
		return "", "", errors.New("至少配置一条 Emby 线路")
	}
	data, err := json.Marshal(lines)
	if err != nil {
		return "", "", err
	}
	return string(data), lines[0].URL, nil
}

func (r *EmbyRemoteService) withLine(cfg *EmbyRemoteConfig, index int) *EmbyRemoteConfig {
	if cfg == nil {
		return nil
	}
	copy := *cfg
	if index >= 0 && index < len(cfg.Lines) {
		copy.BaseURL = normalizeEmbyRemoteURL(cfg.Lines[index].URL)
		copy.ActiveLine = index
	}
	return &copy
}

func (r *EmbyRemoteService) lineOrder(cfg *EmbyRemoteConfig) []int {
	if cfg == nil || len(cfg.Lines) == 0 {
		return []int{0}
	}
	if len(cfg.Lines) == 1 {
		return []int{0}
	}
	active := cfg.ActiveLine
	if active < 0 || active >= len(cfg.Lines) {
		active = 0
	}
	order := []int{active}
	for i := range cfg.Lines {
		if i != active {
			order = append(order, i)
		}
	}
	return order
}

func isEmbyLineFailoverError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// 注意：认证类错误（重认证失败 / 缺少凭据）不在此排除——401 后清空
	// 内存 token 重认证失败时应继续按线路故障转移，其他线路可能存有
	// 自己的 token。仅“登录失败”（密码错误）是账号级问题，无需换线。
	if strings.Contains(msg, "登录失败") ||
		strings.Contains(msg, "未返回 accesstoken") {
		return false
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	return strings.Contains(msg, "连接远程 emby") ||
		strings.Contains(msg, "请求远程 emby 失败") ||
		strings.Contains(msg, "dial tcp") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "i/o timeout")
}

func (r *EmbyRemoteService) persistActiveLine(ctx context.Context, acct *model.StrmAccount, cfg *EmbyRemoteConfig, lineIndex int) error {
	if acct == nil || cfg == nil || lineIndex < 0 || lineIndex >= len(cfg.Lines) {
		return nil
	}
	err := r.updateAccountConfig(ctx, acct, func(raw map[string]string) {
		raw["active_line"] = strconv.Itoa(lineIndex)
		raw["url"] = cfg.Lines[lineIndex].URL
	})
	cfg.ActiveLine = lineIndex
	cfg.BaseURL = normalizeEmbyRemoteURL(cfg.Lines[lineIndex].URL)
	return err
}

func (r *EmbyRemoteService) adoptWorkingLine(ctx context.Context, acct *model.StrmAccount, cfg *EmbyRemoteConfig, lineIndex int) {
	if cfg == nil || lineIndex < 0 || lineIndex >= len(cfg.Lines) {
		return
	}
	if lineIndex == cfg.ActiveLine {
		return
	}
	_ = r.persistActiveLine(ctx, acct, cfg, lineIndex)
}

// LinesOf 返回账号已配置的线路（供管理界面回显，不含敏感信息）。
func (r *EmbyRemoteService) LinesOf(acct *model.StrmAccount) ([]EmbyRemoteLine, int, error) {
	raw := map[string]string{}
	if acct != nil && strings.TrimSpace(acct.Config) != "" {
		if err := json.Unmarshal([]byte(acct.Config), &raw); err != nil {
			return nil, 0, err
		}
	}
	return ParseEmbyRemoteLines(raw)
}
