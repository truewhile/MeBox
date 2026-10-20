package service

import (
	"context"
	"sort"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// 现场（2026-10-16）：13 条媒体记录的 poster_url 指向
// http://mebox-metatube-1:8081/v1/images/primary/... ，而图片代理只放行
// 远程 Emby 挂载主机。MetaTube 是用户显式配置的刮削后端（settings 里的
// adult.scraper.metatube_server），它跑在 docker 私网里，于是每一次封面
// 回源都被连接层 SSRF 校验拒掉，日志刷 warn，前端长期只拿到占位图。
//
// 结论：显式配置的服务地址必须和远程 Emby 一样被加入放行名单。
func TestConfiguredServiceImageHostsExtractsHostnames(t *testing.T) {
	got := configuredServiceImageHosts(
		"http://mebox-metatube-1:8081",
		"https://api.themoviedb.org/3",
		"http://192.168.1.50:8096/emby",
	)
	want := []string{"192.168.1.50", "api.themoviedb.org", "mebox-metatube-1"}
	if len(got) != len(want) {
		t.Fatalf("hosts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("hosts = %v, want %v", got, want)
		}
	}
}

func TestConfiguredServiceImageHostsNormalizesAndDedupes(t *testing.T) {
	got := configuredServiceImageHosts(
		"  HTTP://MeBox-MetaTube-1:8081  ",
		"http://mebox-metatube-1:8081/other/path",
		"mebox-metatube-1:8081",
	)
	if len(got) != 1 || got[0] != "mebox-metatube-1" {
		t.Fatalf("hosts = %v, want exactly [mebox-metatube-1]", got)
	}
}

func TestConfiguredServiceImageHostsSkipsUnusableValues(t *testing.T) {
	got := configuredServiceImageHosts(
		"",
		"   ",
		"not a url at all",
		"http://",
		"://missing-scheme",
		"/relative/path",
		"ftp://files.example.com",
	)
	if len(got) != 0 {
		t.Fatalf("hosts = %v, want none", got)
	}
}

func TestConfiguredServiceImageHostsPreservesIPv6(t *testing.T) {
	got := configuredServiceImageHosts("http://[fd00::5]:8081")
	if len(got) != 1 || got[0] != "fd00::5" {
		t.Fatalf("hosts = %v, want [fd00::5]", got)
	}
}

func TestImageProxyAllowlistCombinesMountsAndConfiguredServices(t *testing.T) {
	// 回归：合并后的名单必须同时覆盖远程 Emby 挂载与显式配置的服务，
	// 否则两组图片里必有一组被 SSRF 校验挡住。
	proxy := newUpstreamImageProxy(t)
	proxy.SetAllowedRemoteHostsProvider(func() []string {
		return mergeImageProxyAllowedHosts(
			[]string{"192.168.1.50"},
			configuredServiceImageHosts("http://mebox-metatube-1:8081"),
		)
	})

	for _, host := range []string{"192.168.1.50", "mebox-metatube-1"} {
		if !proxy.isAllowedRemoteHost(host) {
			t.Fatalf("host %q must bypass the private-IP guard", host)
		}
	}
	if proxy.isAllowedRemoteHost("evil.example.com") {
		t.Fatal("unrelated hosts must stay blocked")
	}
}

func TestMergeImageProxyAllowedHostsDedupesCaseInsensitively(t *testing.T) {
	got := mergeImageProxyAllowedHosts(
		[]string{"192.168.1.50:8096", "MeBox-MetaTube-1"},
		[]string{"mebox-metatube-1", "", "  ", "192.168.1.50"},
	)
	sort.Strings(got)
	want := []string{"192.168.1.50", "mebox-metatube-1"}
	if len(got) != len(want) {
		t.Fatalf("merged = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("merged = %v, want %v", got, want)
		}
	}
}

// 公网上游必须留在名单之外：放行后图片代理会优先走直连，而 GFW 环境恰恰要靠
// HTTP(S)_PROXY 才能取到 image.tmdb.org 这类海报。只有内网地址才需要豁免。
func TestInternalServiceImageHostsKeepsOnlyInternalAddresses(t *testing.T) {
	got := internalServiceImageHosts(
		"http://mebox-metatube-1:8081",
		"http://127.0.0.1:7700",
		"http://192.168.1.50:8096",
		"https://api.themoviedb.org/3",
		"https://image.tmdb.org",
		"https://api.bgm.tv",
		"https://javdb.com",
		"http://nas.local:7700",
		"http://media.internal",
	)
	want := []string{
		"127.0.0.1",
		"192.168.1.50",
		"mebox-metatube-1",
		"media.internal",
		"nas.local",
	}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("hosts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("hosts = %v, want %v", got, want)
		}
	}
}

func TestIsInternalServiceHost(t *testing.T) {
	internal := []string{
		"mebox-metatube-1", "127.0.0.1", "10.1.2.3", "172.18.0.5",
		"192.168.1.50", "fd00::5", "nas.local", "media.internal",
		"box.lan", "db.home.arpa",
	}
	for _, host := range internal {
		if !isInternalServiceHost(host) {
			t.Errorf("isInternalServiceHost(%q) = false, want true", host)
		}
	}
	external := []string{
		"", "api.themoviedb.org", "image.tmdb.org", "www.javbus.com",
		"pics.dmm.co.jp", "8.8.8.8", "example.com",
	}
	for _, host := range external {
		if isInternalServiceHost(host) {
			t.Errorf("isInternalServiceHost(%q) = true, want false", host)
		}
	}
}

// 端到端装配：从真实的 settings / api_configs 里读出内网服务地址。
// 现场数据是 settings.adult.scraper.metatube_server = http://mebox-metatube-1:8081，
// 而 api_configs.metatube.base_url 还是出厂默认的 http://127.0.0.1:7700。
func TestConfiguredServiceImageHostsFromRepoReadsSettingsAndAPIConfigs(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.Setting{}, &model.APIConfig{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := repository.New(db)
	ctx := context.Background()

	if err := repos.Setting.Set(ctx, "adult.scraper.metatube_server", "http://mebox-metatube-1:8081"); err != nil {
		t.Fatal(err)
	}
	if err := repos.Setting.Set(ctx, "adult.scraper.builtin_javdb_url", "https://javdb.com"); err != nil {
		t.Fatal(err)
	}
	for _, row := range []model.APIConfig{
		{Provider: "metatube", BaseURL: "http://127.0.0.1:7700", Enabled: true},
		{Provider: "tmdb", BaseURL: "https://api.themoviedb.org/3", Enabled: true},
		{Provider: "openai", BaseURL: "http://192.168.1.77:11434/v1", Enabled: true},
	} {
		if err := repos.ApiConfig.Create(ctx, &row); err != nil {
			t.Fatal(err)
		}
	}

	got := configuredServiceImageHostsFromRepo(ctx, repos)
	// javdb.com / api.themoviedb.org 是公网地址，必须留在名单之外（否则图片
	// 代理会改走直连，GFW 环境下反而取不到图）；openai 的 base_url 虽然指向
	// 内网，但它从不产出图片 URL，同样不应成为 SSRF 例外。
	want := []string{"127.0.0.1", "mebox-metatube-1"}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("hosts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("hosts = %v, want %v", got, want)
		}
	}
}

// 放行名单是 SSRF 例外，必须收窄：只有「会产出图片 URL」的配置才入选。
func TestConfiguredServiceImageHostsFromRepoIgnoresUnrelatedInternalServices(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.Setting{}, &model.APIConfig{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := repository.New(db)
	ctx := context.Background()

	// 只配置了内网的 OpenAI 兼容网关与内网下载器，没有任何图片来源。
	if err := repos.Setting.Set(ctx, "app.server_url", "http://10.0.0.2:1314"); err != nil {
		t.Fatal(err)
	}
	if err := repos.Setting.Set(ctx, "strm.base_url", "http://10.0.0.2:1314"); err != nil {
		t.Fatal(err)
	}
	for _, row := range []model.APIConfig{
		{Provider: "openai", BaseURL: "http://192.168.1.77:11434/v1", Enabled: true},
		{Provider: "tmdb", BaseURL: "https://api.themoviedb.org/3", Enabled: true},
	} {
		if err := repos.ApiConfig.Create(ctx, &row); err != nil {
			t.Fatal(err)
		}
	}

	if got := configuredServiceImageHostsFromRepo(ctx, repos); len(got) != 0 {
		t.Fatalf("hosts = %v, want none: unrelated internal services must not become SSRF exceptions", got)
	}
}

func TestConfiguredServiceImageHostsFromRepoToleratesMissingRepos(t *testing.T) {
	if got := configuredServiceImageHostsFromRepo(context.Background(), nil); len(got) != 0 {
		t.Fatalf("hosts = %v, want none", got)
	}
}


