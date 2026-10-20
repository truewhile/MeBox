// 在真实 docker 网络里验证图片代理的私网放行名单修复。
//
// 现场（修复前）：MetaTube 刮削结果里的 poster_url 指向
// http://mebox-metatube-1:8081/... ，而放行名单只含远程 Emby 挂载主机，
// 于是每一次封面回源都被连接层 SSRF 校验拒掉，前端长期只拿到占位图。
//
// 本文件用与 service_builder.initImageProxy 相同的装配方式复现那条路径。
// 真实回源那一步只在能解析到 MetaTube 服务名的环境里跑（生产容器网络）；
// 开发机上会 Skip，因为那属于环境缺失，不是回归。
//
// 在生产机上运行的姿势（无需 Go 工具链）：
//
//	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/verify.test ./internal/service
//	scp /tmp/verify.test root@HOST:/tmp/ && ssh root@HOST \
//	  'docker cp /tmp/verify.test mebox:/tmp/ && docker exec mebox /tmp/verify.test \
//	   -test.run TestImageProxyFetchesFromMetaTubeService -test.v'
package service

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// 与生产环境一致的两个取值（取自 10.0.0.2 上真实的 settings / api_configs）。
const (
	metaTubeServer = "http://mebox-metatube-1:8081"
	metaTubeLegacy = "http://127.0.0.1:7700"
	sampleImageURL = "http://mebox-metatube-1:8081/v1/images/primary/JAV321/dsam-15"
)

// newMetaTubeVerificationRepos 建一个带现场取值的临时库（不碰生产数据）。
func newMetaTubeVerificationRepos(t *testing.T) (*repository.Container, *ImageProxy) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.Setting{}, &model.APIConfig{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := repository.New(db)
	ctx := context.Background()
	if err := repos.Setting.Set(ctx, "adult.scraper.metatube_server", metaTubeServer); err != nil {
		t.Fatalf("seed setting: %v", err)
	}
	if err := repos.ApiConfig.Create(ctx, &model.APIConfig{
		Provider: "metatube", BaseURL: metaTubeLegacy, Enabled: true,
	}); err != nil {
		t.Fatalf("seed metatube api config: %v", err)
	}
	// 一个公网上游，用来确认它没有混进放行名单。
	if err := repos.ApiConfig.Create(ctx, &model.APIConfig{
		Provider: "tmdb", BaseURL: "https://api.themoviedb.org/3", Enabled: true,
	}); err != nil {
		t.Fatalf("seed tmdb api config: %v", err)
	}

	cfg := &config.Config{Cache: config.CacheConfig{CacheDir: filepath.Join(t.TempDir(), "cache")}}
	cfg.Secrets.JWTSecret = "verify-secret"
	proxy := NewImageProxy(cfg, zap.NewNop())
	// 与 service_builder.initImageProxy 相同的装配方式（无远程 Emby 挂载：
	// 本次只验证「显式配置的服务地址」这条路径）。
	proxy.SetAllowedRemoteHostsProvider(func() []string {
		return mergeImageProxyAllowedHosts(
			nil,
			configuredServiceImageHostsFromRepo(ctx, repos),
		)
	})
	return repos, proxy
}

// TestImageProxyAllowlistIncludesConfiguredMetaTube 确认内网服务地址被放行、
// 公网上游没有被放行。
func TestImageProxyAllowlistIncludesConfiguredMetaTube(t *testing.T) {
	repos, proxy := newMetaTubeVerificationRepos(t)
	t.Logf("allowlist from config: %v",
		configuredServiceImageHostsFromRepo(context.Background(), repos))

	for _, host := range []string{"mebox-metatube-1", "127.0.0.1"} {
		if !proxy.isAllowedRemoteHost(host) {
			t.Fatalf("host %q must be allowlisted (it is an explicitly configured service)", host)
		}
	}
	if proxy.isAllowedRemoteHost("api.themoviedb.org") {
		t.Fatal("public upstreams must stay off the allowlist so image fetches keep using the proxy")
	}
}

// TestImageProxyReachesMetaTubeService 证明放行名单真的解除了私网拦截。
//
// 现场（修复前）回源必然失败于
//
//	dial tcp 172.18.0.5:8081: image proxy: requests to private/internal hosts are not allowed
//
// 也就是连接根本没发生。修复后连接成功，失败只可能来自上游自己的 HTTP 状态
// （MetaTube 冷缓存时对未刮削过的番号返回 500）。
//
// 因此这个用例断言的不是「拿到 200」，而是**「不再是被私网校验拒绝」**：
// 后者才是本次修复的对象，前者取决于 MetaTube 缓存里有没有那条记录。
func TestImageProxyReachesMetaTubeService(t *testing.T) {
	_, proxy := newMetaTubeVerificationRepos(t)

	data, ctype, err := proxy.Fetch(context.Background(), sampleImageURL)
	if err != nil {
		if isMetaTubeUnreachable(err) {
			t.Skipf("mebox-metatube-1 is not reachable from this environment: %v", err)
		}
		if isPrivateHostRejection(err) {
			t.Fatalf("the allowlist did not take effect: %v", err)
		}
		// 连接已经建立，只是上游自己返回了错误状态（MetaTube 冷缓存）。
		t.Logf("reached metaTube; upstream returned its own error: %v", err)
		return
	}
	t.Logf("fetched %d bytes (content-type=%s)", len(data), ctype)
	if len(data) == 0 {
		t.Fatal("fetched an empty image body")
	}
}

// isPrivateHostRejection 报告错误是否来自连接层的私网校验。
// 这正是本次修复必须消除的那一类失败。
func isPrivateHostRejection(err error) bool {
	return err != nil && strings.Contains(err.Error(), "requests to private/internal hosts are not allowed")
}

// isMetaTubeUnreachable 区分「环境里没有这个服务」与「放行名单没生效」。
func isMetaTubeUnreachable(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{"no such host", "server misbehaving", "i/o timeout", "connection refused"} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}
