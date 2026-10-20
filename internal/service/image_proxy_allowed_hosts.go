package service

import (
	"context"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/truewhile/MeBox/internal/repository"
)

// 显式配置的服务地址也要进入图片代理的私网放行名单。
//
// 背景（现场 2026-10-16）：MetaTube 刮削后端被配置成 docker 服务名
// `http://mebox-metatube-1:8081`，刮削结果里的 poster_url 就是这个地址。
// 图片代理的连接层 SSRF 校验只放行远程 Emby 挂载主机，于是每一次封面回源都
// 被拒（日志 warn: requests to private/internal hosts are not allowed），
// 前端长期只拿到占位图。
//
// 这里的取舍与 isPrivateHost 的注释一致：私网校验防的是「用户可控 URL 打到
// 内网」，而管理员显式配置的服务地址属于受信输入，和远程 Emby 挂载同级。
func configuredServiceImageHosts(values ...string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		host := imageHostFromBaseURL(raw)
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		out = append(out, host)
	}
	sort.Strings(out)
	return out
}

// internalServiceImageHosts 只保留指向内网/局域网的服务地址。
//
// 公网域名（image.tmdb.org、api.themoviedb.org 等）必须留在名单之外：放行后
// 图片代理会优先走直连客户端，而 GFW 环境恰恰需要先用 HTTP(S)_PROXY。把它们
// 加进来会让每张海报都白付一次直连失败。
func internalServiceImageHosts(values ...string) []string {
	hosts := configuredServiceImageHosts(values...)
	out := make([]string, 0, len(hosts))
	for _, host := range hosts {
		if isInternalServiceHost(host) {
			out = append(out, host)
		}
	}
	return out
}

// isInternalServiceHost 判断主机名是否只在内网可达：私网/回环/链路本地 IP、
// 单标签服务名（docker compose 服务名如 mebox-metatube-1），或常见的内部域名后缀。
func isInternalServiceHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return isPrivateIP(ip)
	}
	// 单标签名（不含点）不是公网可解析的 FQDN，只可能是容器名/主机名。
	if !strings.Contains(host, ".") {
		return true
	}
	for _, suffix := range []string{".local", ".internal", ".lan", ".home.arpa", ".localhost"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// imageHostFromBaseURL 从配置里的 base URL 取出小写主机名（不含端口）。
// 容忍缺少 scheme 的写法（`mebox-metatube-1:8081`），并拒绝非 http/https。
func imageHostFromBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return ""
	}
	return strings.ToLower(strings.TrimSpace(u.Hostname()))
}

// mergeImageProxyAllowedHosts 合并多组放行主机（远程 Emby 挂载 + 显式配置的
// 服务地址），去重时忽略大小写与端口。
func mergeImageProxyAllowedHosts(groups ...[]string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, group := range groups {
		for _, raw := range group {
			host := strings.ToLower(strings.TrimSpace(raw))
			if host == "" {
				continue
			}
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = strings.ToLower(strings.TrimSpace(h))
			}
			if host == "" {
				continue
			}
			if _, ok := seen[host]; ok {
				continue
			}
			seen[host] = struct{}{}
			out = append(out, host)
		}
	}
	sort.Strings(out)
	return out
}

// imageMintingSettingKeys 是「其取值会被拼进图片 URL」的设置项。
//
// 只收录这类设置：放行名单每多一条就是多一处 SSRF 例外，因此 app.server_url、
// strm.base_url 这些只出现在播放地址里的设置不在此列（它们由 streamURL 使用，
// 与 /api/img 无关）。
var imageMintingSettingKeys = []string{
	"adult.scraper.metatube_server",
	"adult.scraper.builtin_javdb_url",
	"adult.scraper.builtin_javbus_url",
}

// imageMintingAPIProviders 是「其 base_url 会被拼进图片 URL」的 api_configs 行。
//
// 同样刻意收窄：api_configs 里每个 provider 都有一行出厂默认值，其中 metatube
// 默认指向 http://127.0.0.1:7700。把整张表都放行会让每次安装都无条件放开回环
// 地址，而只有真正用 MetaTube 刮削的部署才需要这个例外。
var imageMintingAPIProviders = []string{"metatube"}

// configuredServiceImageHostsFromRepo 从持久化配置里收集「会被写进图片 URL」的
// 内网服务地址。只在构造阶段调用一次，所以直接读库、忽略错误——读不到只是少
// 一条放行，不影响启动。
func configuredServiceImageHostsFromRepo(ctx context.Context, repos *repository.Container) []string {
	if repos == nil {
		return nil
	}
	values := make([]string, 0, 4)

	if repos.Setting != nil {
		for _, key := range imageMintingSettingKeys {
			if val, err := repos.Setting.Get(ctx, key); err == nil && strings.TrimSpace(val) != "" {
				values = append(values, val)
			}
		}
	}

	if repos.ApiConfig != nil {
		// 用 List 而不是 FindByProvider：后者走 First()，缺行时 gorm 会打一条
		// "record not found" 日志，而「没配置 MetaTube」是完全正常的状态。
		if rows, err := repos.ApiConfig.List(ctx); err == nil {
			for _, row := range rows {
				if !imageMintingAPIProvider(row.Provider) {
					continue
				}
				if strings.TrimSpace(row.BaseURL) != "" {
					values = append(values, row.BaseURL)
				}
				// Extra 是逗号/分号分隔的备用源列表（见 api_config.go 的 adult 默认值）。
				values = append(values, adultConfiguredBases(row.Extra)...)
			}
		}
	}

	return internalServiceImageHosts(values...)
}

func imageMintingAPIProvider(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	for _, allowed := range imageMintingAPIProviders {
		if provider == allowed {
			return true
		}
	}
	return false
}

