package service

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service/cloud"
)

// overrideDanmakuOfficialBase points the "official" endpoint (match + fallback)
// at a local server for the duration of a test.
func overrideDanmakuOfficialBase(t *testing.T, base string) {
	t.Helper()
	old := danmakuOfficialBase
	danmakuOfficialBase = base
	t.Cleanup(func() { danmakuOfficialBase = old })
}

// writeDanmakuTestVideo writes a deterministic <16MB video-ish file and
// returns its path and the expected dandanplay hash (MD5 of the whole file,
// since the file is smaller than the 16MB prefix).
func writeDanmakuTestVideo(t *testing.T, name string) (path, wantHash string) {
	t.Helper()
	content := bytes.Repeat([]byte("MeBox-danmaku-hash-test-0123456789"), 500)
	path = filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, content, 0o644))
	sum := md5.Sum(content)
	return path, hex.EncodeToString(sum[:])
}

// danmakuOfficialServer serves /api/v2/match (with the given payload) and a
// comment library for the matched episode.
func danmakuOfficialServer(t *testing.T, matchBody, commentBody string, seen *string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/match", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if seen != nil {
			*seen = string(b)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, matchBody)
	})
	mux.HandleFunc("/api/v2/comment/25484", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, commentBody)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// seedDanmakuVideoMedia inserts a media row with a real path (for the hash
// layer) and optional episode number.
func seedDanmakuVideoMedia(t *testing.T, svc *DanmakuService, id, title, path string, size int64, episode int) {
	t.Helper()
	m := model.Media{Title: title, Path: path, SizeBytes: size, EpisodeNum: episode}
	m.ID = id
	require.NoError(t, svc.repo.DB.Create(&m).Error)
}

// 第 1 层：本地文件直接算 hash → 官方 /api/v2/match → 命中后拉弹幕。
func TestDanmakuFetchHashMatchLayer(t *testing.T) {
	videoPath, wantHash := writeDanmakuTestVideo(t, "测试动画.第01话.mkv")
	var seen string
	official := danmakuOfficialServer(t,
		`{"success":true,"isMatched":true,"matches":[{"episodeId":25484,"animeId":1001,"animeTitle":"测试动画","episodeTitle":"第1话"}]}`,
		`<?xml version="1.0"?><i><d p="0.5,1,16777215,user1">弹幕Hash命中</d></i>`,
		&seen)
	overrideDanmakuOfficialBase(t, official.URL)

	svc := newDanmakuTestService(t)
	ctx := context.Background()
	seedDanmakuVideoMedia(t, svc, "mH", "测试动画", videoPath, 32000, 1)

	res, err := svc.Fetch(ctx, "mH", "", "")
	require.NoError(t, err)
	require.True(t, res.Enabled)
	require.Equal(t, "xml", res.SourceType)
	require.Contains(t, res.Raw, "弹幕Hash命中")
	require.Empty(t, res.Candidates)
	require.Equal(t, "测试动画", res.AnimeTitle)
	require.Equal(t, "第1话", res.EpisodeTitle)
	require.Equal(t, int64(25484), res.EpisodeID)
	require.Equal(t, "hash", res.MatchMode)

	// match 请求体：文件名去扩展名并 URL 转义（官方接口要求，实测验证）、
	// hash、大小、matchMode 齐全。
	require.Contains(t, seen, `"fileName":"`+url.QueryEscape("测试动画.第01话")+`"`)
	require.Contains(t, seen, `"fileHash":"`+wantHash+`"`)
	require.Contains(t, seen, `"fileSize":32000`)
	require.Contains(t, seen, `"matchMode":"hashAndFileName"`)
}

// 第 1 层拉弹幕：官方 match 给出的 episodeId 属于官方 ID 空间，不能直接拿去
// 请求第三方源（真实源上只会 404）。必须用官方给到的剧名+集数在配置源里重定位
// 到配置源自己的 episodeId，再用它拉弹幕。
func TestDanmakuFetchHashMatchRemapsEpisodeIDToConfiguredSource(t *testing.T) {
	videoPath, _ := writeDanmakuTestVideo(t, "测试动画.第01话.mkv")

	// 配置源使用自己的 ID 空间：官方是 90001，配置源是 25484。
	// 副标题一致，用于跨源确认是同一集。
	const subtitle = "测试副标题"
	cfgMux := http.NewServeMux()
	cfgMux.HandleFunc("/api/v2/search/episodes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"hasMore":false,"animes":[{"animeId":1001,"animeTitle":"测试动画","episodes":[{"episodeId":25484,"episodeTitle":"第1话 `+subtitle+`"}]}]}`)
	})
	cfgMux.HandleFunc("/api/v2/comment/25484", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0"?><i><d p="0.5,1,16777215,user1">弹幕A</d></i>`)
	})
	cfgSrv := httptest.NewServer(cfgMux)
	t.Cleanup(cfgSrv.Close)

	officialMux := http.NewServeMux()
	officialMux.HandleFunc("/api/v2/match", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"success":true,"isMatched":true,"matches":[{"episodeId":90001,"animeId":1001,"animeTitle":"测试动画","episodeTitle":"第1话 `+subtitle+`"}]}`)
	})
	officialMux.HandleFunc("/api/v2/comment/90001", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0"?><i><d p="0.5,1,16777215,user1">弹幕B官方</d></i>`)
	})
	official := httptest.NewServer(officialMux)
	t.Cleanup(official.Close)
	overrideDanmakuOfficialBase(t, official.URL)

	svc := newDanmakuTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuSourceKey, cfgSrv.URL))
	seedDanmakuVideoMedia(t, svc, "mC", "测试动画", videoPath, 32000, 0)

	res, err := svc.Fetch(ctx, "mC", "", "")
	require.NoError(t, err)
	// 弹幕取自配置源，且用的是重定位后的 ID。
	require.Contains(t, res.Raw, "弹幕A")
	require.NotContains(t, res.Raw, "弹幕B官方")
	require.Equal(t, int64(25484), res.EpisodeID)
	require.Equal(t, "hash", res.MatchMode)
}

// 配置源能定位到该集，但返回的是空弹幕库（count=0）时必须回官方兜底：
// 第三方目录里有条目不代表真的收录了弹幕。
func TestDanmakuFetchHashMatchFallsBackWhenConfiguredLibraryIsEmpty(t *testing.T) {
	videoPath, _ := writeDanmakuTestVideo(t, "测试动画.第01话.mkv")

	const subtitle = "测试副标题"
	cfgMux := http.NewServeMux()
	cfgMux.HandleFunc("/api/v2/search/episodes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"hasMore":false,"animes":[{"animeId":1001,"animeTitle":"测试动画","episodes":[{"episodeId":25484,"episodeTitle":"第1话 `+subtitle+`"}]}]}`)
	})
	// 该集在配置源上存在，但弹幕为空。
	cfgMux.HandleFunc("/api/v2/comment/25484", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"count":0,"comments":[]}`)
	})
	cfgSrv := httptest.NewServer(cfgMux)
	t.Cleanup(cfgSrv.Close)

	officialMux := http.NewServeMux()
	officialMux.HandleFunc("/api/v2/match", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"success":true,"isMatched":true,"matches":[{"episodeId":90001,"animeId":1001,"animeTitle":"测试动画","episodeTitle":"第1话 `+subtitle+`"}]}`)
	})
	officialMux.HandleFunc("/api/v2/comment/90001", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0"?><i><d p="0.5,1,16777215,user1">官方兜底弹幕</d></i>`)
	})
	official := httptest.NewServer(officialMux)
	t.Cleanup(official.Close)
	overrideDanmakuOfficialBase(t, official.URL)

	svc := newDanmakuTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuSourceKey, cfgSrv.URL))
	seedDanmakuVideoMedia(t, svc, "mEmpty", "测试动画", videoPath, 32000, 0)

	res, err := svc.Fetch(ctx, "mEmpty", "", "")
	require.NoError(t, err)
	require.Contains(t, res.Raw, "官方兜底弹幕")
	require.Equal(t, int64(90001), res.EpisodeID)
}

// 同一集在配置源里命中多个来源时：自动加载第一条，其余作为可切换来源返回，
// 让用户能在面板里直接切换，而不必重新搜索。
func TestDanmakuFetchHashMatchReturnsAlternatives(t *testing.T) {
	videoPath, _ := writeDanmakuTestVideo(t, "多来源动画.第01话.mkv")

	const subtitle = "同一个副标题"
	cfgMux := http.NewServeMux()
	cfgMux.HandleFunc("/api/v2/search/episodes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 三个来源都指向同一集（副标题与集数一致），模拟 LogVar 聚合多站。
		fmt.Fprint(w, `{"hasMore":false,"animes":[`+
			`{"animeId":1,"animeTitle":"多来源动画 from dandan","episodes":[{"episodeId":101,"episodeTitle":"第1话 `+subtitle+`"}]},`+
			`{"animeId":2,"animeTitle":"多来源动画 from bilibili","episodes":[{"episodeId":102,"episodeTitle":"第1话 `+subtitle+`"}]},`+
			`{"animeId":3,"animeTitle":"多来源动画 from qq","episodes":[{"episodeId":103,"episodeTitle":"第1话 `+subtitle+`"}]}]}`)
	})
	cfgMux.HandleFunc("/api/v2/comment/101", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0"?><i><d p="0.5,1,16777215,user1">首选来源弹幕</d></i>`)
	})
	cfgSrv := httptest.NewServer(cfgMux)
	t.Cleanup(cfgSrv.Close)

	officialMux := http.NewServeMux()
	officialMux.HandleFunc("/api/v2/match", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"success":true,"isMatched":true,"matches":[{"episodeId":90001,"animeId":9,"animeTitle":"多来源动画","episodeTitle":"第1话 `+subtitle+`"}]}`)
	})
	officialMux.HandleFunc("/api/v2/comment/90001", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0"?><i><d p="0.5,1,16777215,user1">官方弹幕</d></i>`)
	})
	official := httptest.NewServer(officialMux)
	t.Cleanup(official.Close)
	overrideDanmakuOfficialBase(t, official.URL)

	svc := newDanmakuTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuSourceKey, cfgSrv.URL))
	seedDanmakuVideoMedia(t, svc, "mAlt", "多来源动画", videoPath, 32000, 0)

	res, err := svc.Fetch(ctx, "mAlt", "", "")
	require.NoError(t, err)

	// 自动加载第一个来源（沿用既有取值逻辑）。
	require.Contains(t, res.Raw, "首选来源弹幕")
	require.Equal(t, int64(101), res.EpisodeID)
	require.Equal(t, "hash", res.MatchMode)

	// 三个来源全部作为可切换列表返回。
	require.Len(t, res.Alternatives, 3)
	var ids []int64
	for _, a := range res.Alternatives {
		for _, e := range a.Episodes {
			ids = append(ids, e.EpisodeID)
		}
	}
	require.Equal(t, []int64{101, 102, 103}, ids)

	// Candidates 的语义必须保持不变（这里不是"必须选择"），否则前端会停止自动加载。
	require.Empty(t, res.Candidates)
}

// 只有一个来源时不应产生 alternatives，避免面板出现无意义的单条列表。
func TestDanmakuFetchHashMatchNoAlternativesForSingleSource(t *testing.T) {
	videoPath, _ := writeDanmakuTestVideo(t, "单来源动画.第01话.mkv")

	const subtitle = "唯一副标题"
	cfgMux := http.NewServeMux()
	cfgMux.HandleFunc("/api/v2/search/episodes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"hasMore":false,"animes":[{"animeId":1,"animeTitle":"单来源动画","episodes":[{"episodeId":201,"episodeTitle":"第1话 `+subtitle+`"}]}]}`)
	})
	cfgMux.HandleFunc("/api/v2/comment/201", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0"?><i><d p="0.5,1,16777215,user1">唯一来源弹幕</d></i>`)
	})
	cfgSrv := httptest.NewServer(cfgMux)
	t.Cleanup(cfgSrv.Close)

	officialMux := http.NewServeMux()
	officialMux.HandleFunc("/api/v2/match", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"success":true,"isMatched":true,"matches":[{"episodeId":90002,"animeId":9,"animeTitle":"单来源动画","episodeTitle":"第1话 `+subtitle+`"}]}`)
	})
	official := httptest.NewServer(officialMux)
	t.Cleanup(official.Close)
	overrideDanmakuOfficialBase(t, official.URL)

	svc := newDanmakuTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuSourceKey, cfgSrv.URL))
	seedDanmakuVideoMedia(t, svc, "mOne", "单来源动画", videoPath, 32000, 0)

	res, err := svc.Fetch(ctx, "mOne", "", "")
	require.NoError(t, err)
	require.Contains(t, res.Raw, "唯一来源弹幕")
	require.Empty(t, res.Alternatives)
}

// 开启合并后：同一集的多个来源被合并，重复弹幕（时间+内容相同）只保留一条。
func TestDanmakuFetchMergeSourcesCombinesAndDeduplicates(t *testing.T) {
	videoPath, _ := writeDanmakuTestVideo(t, "合并动画.第01话.mkv")

	const subtitle = "同一副标题"
	cfgMux := http.NewServeMux()
	cfgMux.HandleFunc("/api/v2/search/episodes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"hasMore":false,"animes":[`+
			`{"animeId":1,"animeTitle":"来源A","episodes":[{"episodeId":301,"episodeTitle":"第1话 `+subtitle+`"}]},`+
			`{"animeId":2,"animeTitle":"来源B","episodes":[{"episodeId":302,"episodeTitle":"第1话 `+subtitle+`"}]}]}`)
	})
	// A 与 B 各有一条重复弹幕（1.0 秒「重复弹幕」）和各自独有的一条。
	cfgMux.HandleFunc("/api/v2/comment/301", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"count":2,"comments":[`+
			`{"p":"1.00,1,16777215,u1","m":"重复弹幕"},`+
			`{"p":"2.00,1,16777215,u1","m":"只在A"}]}`)
	})
	cfgMux.HandleFunc("/api/v2/comment/302", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"count":2,"comments":[`+
			`{"p":"1.00,1,16777215,u2","m":"重复弹幕"},`+
			`{"p":"3.00,1,16777215,u2","m":"只在B"}]}`)
	})
	cfgSrv := httptest.NewServer(cfgMux)
	t.Cleanup(cfgSrv.Close)

	officialMux := http.NewServeMux()
	officialMux.HandleFunc("/api/v2/match", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"success":true,"isMatched":true,"matches":[{"episodeId":90003,"animeId":9,"animeTitle":"合并动画","episodeTitle":"第1话 `+subtitle+`"}]}`)
	})
	official := httptest.NewServer(officialMux)
	t.Cleanup(official.Close)
	overrideDanmakuOfficialBase(t, official.URL)

	svc := newDanmakuTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuSourceKey, cfgSrv.URL))
	seedDanmakuVideoMedia(t, svc, "mMerge", "合并动画", videoPath, 32000, 0)

	res, err := svc.FetchWithOptions(ctx, "mMerge", "", "", DanmakuFetchOptions{MergeSources: true})
	require.NoError(t, err)
	require.Equal(t, 2, res.MergedSources, "expected both sources to be merged")

	merged := parseDanmakuComments(res.Raw)
	require.Len(t, merged, 3, "duplicate comment must collapse: got %#v", merged)
	require.Equal(t, "重复弹幕", merged[0].Text)
	require.Equal(t, 1.0, merged[0].TimeSec)
	require.Equal(t, "只在A", merged[1].Text)
	require.Equal(t, "只在B", merged[2].Text)
	// 合并结果用 JSON 输出，前端据此选择解析分支。
	require.Equal(t, "json", res.SourceType)
}

// 未开启合并时，行为与原来一致：只加载自动选中的那一个来源。
func TestDanmakuFetchWithoutMergeLoadsSingleSource(t *testing.T) {
	videoPath, _ := writeDanmakuTestVideo(t, "不合并动画.第01话.mkv")

	const subtitle = "同一副标题"
	cfgMux := http.NewServeMux()
	cfgMux.HandleFunc("/api/v2/search/episodes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"hasMore":false,"animes":[`+
			`{"animeId":1,"animeTitle":"来源A","episodes":[{"episodeId":401,"episodeTitle":"第1话 `+subtitle+`"}]},`+
			`{"animeId":2,"animeTitle":"来源B","episodes":[{"episodeId":402,"episodeTitle":"第1话 `+subtitle+`"}]}]}`)
	})
	cfgMux.HandleFunc("/api/v2/comment/401", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"count":1,"comments":[{"p":"1.00,1,16777215,u1","m":"只在A"}]}`)
	})
	cfgMux.HandleFunc("/api/v2/comment/402", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"count":1,"comments":[{"p":"1.00,1,16777215,u2","m":"只在B"}]}`)
	})
	cfgSrv := httptest.NewServer(cfgMux)
	t.Cleanup(cfgSrv.Close)

	officialMux := http.NewServeMux()
	officialMux.HandleFunc("/api/v2/match", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"success":true,"isMatched":true,"matches":[{"episodeId":90004,"animeId":9,"animeTitle":"不合并动画","episodeTitle":"第1话 `+subtitle+`"}]}`)
	})
	official := httptest.NewServer(officialMux)
	t.Cleanup(official.Close)
	overrideDanmakuOfficialBase(t, official.URL)

	svc := newDanmakuTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuSourceKey, cfgSrv.URL))
	seedDanmakuVideoMedia(t, svc, "mNoMerge", "不合并动画", videoPath, 32000, 0)

	res, err := svc.Fetch(ctx, "mNoMerge", "", "")
	require.NoError(t, err)
	require.Zero(t, res.MergedSources)
	require.Equal(t, int64(401), res.EpisodeID)
	// 只应有自动选中来源的弹幕。
	require.Contains(t, res.Raw, "只在A")
	require.NotContains(t, res.Raw, "只在B")
	// 两个来源仍然作为可切换项返回（合并开关不影响候选列表）。
	require.Len(t, res.Alternatives, 2)
}

// 合并偏好按用户持久化。
func TestDanmakuMergeSourcesPreferencePersistsPerUser(t *testing.T) {
	svc := newDanmakuTestService(t)
	ctx := context.Background()

	userA := model.User{Username: "merge-user-a", PasswordHash: "x", Role: "user", IsActive: true}
	userA.ID = "user-a"
	userB := model.User{Username: "merge-user-b", PasswordHash: "x", Role: "user", IsActive: true}
	userB.ID = "user-b"
	require.NoError(t, svc.repo.User.Create(ctx, &userA))
	require.NoError(t, svc.repo.User.Create(ctx, &userB))

	require.False(t, svc.MergeSourcesEnabled(ctx, "user-a"))
	require.NoError(t, svc.SetMergeSources(ctx, "user-a", true))
	require.True(t, svc.MergeSourcesEnabled(ctx, "user-a"))
	// 另一个用户不受影响。
	require.False(t, svc.MergeSourcesEnabled(ctx, "user-b"))

	// 重新读取确认已落库，且 ConfigForUser 会带出该偏好。
	cfg := svc.ConfigForUser(ctx, "user-a")
	require.True(t, cfg.MergeSources)
	require.False(t, svc.ConfigForUser(ctx, "user-b").MergeSources)
}

// 配置源搜不到对应剧集时必须回退官方：用官方自身的 episodeId 请求官方，
// 而不是拿官方 ID 去撞配置源。
func TestDanmakuFetchHashMatchFallsBackToOfficialWhenConfiguredHasNoMatch(t *testing.T) {
	videoPath, _ := writeDanmakuTestVideo(t, "冷门动画.第01话.mkv")

	cfgMux := http.NewServeMux()
	cfgMux.HandleFunc("/api/v2/search/episodes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"hasMore":false,"animes":[]}`)
	})
	cfgSrv := httptest.NewServer(cfgMux)
	t.Cleanup(cfgSrv.Close)

	officialMux := http.NewServeMux()
	officialMux.HandleFunc("/api/v2/match", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"success":true,"isMatched":true,"matches":[{"episodeId":90001,"animeId":1001,"animeTitle":"冷门动画","episodeTitle":"第1话 无人知晓"}]}`)
	})
	officialMux.HandleFunc("/api/v2/comment/90001", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0"?><i><d p="0.5,1,16777215,user1">弹幕来自官方</d></i>`)
	})
	official := httptest.NewServer(officialMux)
	t.Cleanup(official.Close)
	overrideDanmakuOfficialBase(t, official.URL)

	svc := newDanmakuTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuSourceKey, cfgSrv.URL))
	seedDanmakuVideoMedia(t, svc, "mNoMatch", "冷门动画", videoPath, 32000, 0)

	res, err := svc.Fetch(ctx, "mNoMatch", "", "")
	require.NoError(t, err)
	require.Contains(t, res.Raw, "弹幕来自官方")
	require.Equal(t, int64(90001), res.EpisodeID)
}

func TestDanmakuFetchHashMatchConfiguredFailsFallsBackOfficial(t *testing.T) {
	videoPath, _ := writeDanmakuTestVideo(t, "测试动画.第01话.mkv")

	// 配置源：搜索正常，但弹幕接口 500。
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/search/episodes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"hasMore":false,"animes":[{"animeId":1001,"animeTitle":"测试动画","episodes":[{"episodeId":25484,"episodeTitle":"第1话"}]}]}`)
	})
	mux.HandleFunc("/api/v2/comment/25484", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	cfgSrv := httptest.NewServer(mux)
	t.Cleanup(cfgSrv.Close)

	official := danmakuOfficialServer(t,
		`{"success":true,"isMatched":true,"matches":[{"episodeId":25484,"animeId":1001,"animeTitle":"测试动画"}]}`,
		`<?xml version="1.0"?><i><d p="0.5,1,16777215,user1">弹幕官方兜底</d></i>`,
		nil)
	overrideDanmakuOfficialBase(t, official.URL)

	svc := newDanmakuTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuSourceKey, cfgSrv.URL))
	seedDanmakuVideoMedia(t, svc, "mF", "测试动画", videoPath, 32000, 0)

	res, err := svc.Fetch(ctx, "mF", "", "")
	require.NoError(t, err)
	require.Contains(t, res.Raw, "弹幕官方兜底")
}

// 第 1 层未命中时，即使官方附带模糊候选，也不能把第一条当作精准匹配；
// 应继续走第 2 层按文件名+集数搜索。
func TestDanmakuFetchHashMissFallsBackToFileNameSearch(t *testing.T) {
	videoPath, _ := writeDanmakuTestVideo(t, "测试动画.第01话.mkv")

	cfgSrv := newDanmakuSourceServer(t) // 搜索 + 弹幕A
	official := danmakuOfficialServer(t,
		`{"success":true,"isMatched":false,"matches":[{"episodeId":120140001,"animeId":12014,"animeTitle":"91天","episodeTitle":"第1话 杀人之夜"}]}`,
		`<i></i>`, nil)
	overrideDanmakuOfficialBase(t, official.URL)

	svc := newDanmakuTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuSourceKey, cfgSrv.URL()))
	seedDanmakuVideoMedia(t, svc, "mM", "刮削标题", videoPath, 32000, 1)

	res, err := svc.Fetch(ctx, "mM", "", "")
	require.NoError(t, err)
	require.True(t, res.Enabled)
	require.Contains(t, res.Raw, "弹幕A")
	require.Equal(t, "filename", res.MatchMode)
	require.Equal(t, "测试动画", res.AnimeTitle)
	// 第 2 层命中：搜索请求按文件名进行，而不是误用官方第一条模糊候选。
	query, err := url.ParseQuery(cfgSrv.lastSearch)
	require.NoError(t, err)
	require.Contains(t, query.Get("anime"), "测试动画.第01话")
	require.NotContains(t, query.Get("anime"), "91天")
}

// hash 未精确命中时，若本地已有刮削的剧名、年份、集数和集标题，应先用这些
// 元数据锁定正确来源，而不是直接进入大量候选的手工选择。
func TestDanmakuFetchHashMissUsesScrapedMetadata(t *testing.T) {
	videoPath, _ := writeDanmakuTestVideo(t, "local-release-name.mkv")
	cfgSrv := newDanmakuSourceServerWithSearch(t,
		`{"hasMore":false,"animes":[`+
			`{"animeId":1,"animeTitle":"命运石之门 0(2018)【TV动画】from dandan&animeko","episodes":[{"episodeId":120140001,"episodeTitle":"【dandan&animeko】 第1话 零化域的缺失之环-Absolute Zero-"}]},`+
			`{"animeId":2,"animeTitle":"命运石之门(2011)【TV动画】from dandan&animeko","episodes":[{"episodeId":25484,"episodeTitle":"【dandan&animeko】 第1话 始与终的序章-Turning Point-"}]},`+
			`{"animeId":3,"animeTitle":"命运石之门(2011)【动漫】from 360","episodes":[{"episodeId":120140002,"episodeTitle":"【qq】 第1集"}]}`+
			`]}`)
	official := danmakuOfficialServer(t,
		`{"success":true,"isMatched":false,"matches":[{"episodeId":120140001,"animeId":12014,"animeTitle":"91天","episodeTitle":"第1话 杀人之夜"}]}`,
		`<i></i>`, nil)
	overrideDanmakuOfficialBase(t, official.URL)

	svc := newDanmakuTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.repo.Setting.Set(ctx, DanmakuSourceKey, cfgSrv.URL()))
	media := model.Media{
		Title:        "命运石之门",
		Year:         2011,
		EpisodeNum:   1,
		EpisodeTitle: "起始与终结的序章",
		Path:         videoPath,
		SizeBytes:    32000,
		ScrapeStatus: "matched",
	}
	media.ID = "mScrapedMeta"
	require.NoError(t, svc.repo.DB.Create(&media).Error)

	res, err := svc.Fetch(ctx, media.ID, "", "")
	require.NoError(t, err)
	require.True(t, res.Enabled)
	require.Contains(t, res.Raw, "弹幕A")
	require.Equal(t, "metadata", res.MatchMode)
	require.Equal(t, "命运石之门(2011)【TV动画】from dandan&animeko", res.AnimeTitle)
	require.Equal(t, int64(25484), res.EpisodeID)
	require.Empty(t, res.Candidates)
}

// strm：通过解析出的直链 Range 拉 16MB 前缀算 hash → match → 拉弹幕。
// range server 模拟 115 CDN 防盗链：UA 不匹配直接 403（真实部署中
// 直链绑定换取时的 UA，不带绑定 UA 拉取会失败，见 pan115_openapi.go）。
func TestDanmakuFetchStrmHashViaDirectLink(t *testing.T) {
	content := bytes.Repeat([]byte("strm-video-bytes-0123456789"), 400)
	sum := md5.Sum(content)
	wantHash := hex.EncodeToString(sum[:])

	var gotRange, gotUA string
	rangeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		gotUA = r.Header.Get("User-Agent")
		if gotUA != "bound-ua-115" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(content)
	}))
	t.Cleanup(rangeSrv.Close)

	var seen string
	official := danmakuOfficialServer(t,
		`{"success":true,"isMatched":true,"matches":[{"episodeId":25484,"animeId":1001,"animeTitle":"远程动画"}]}`,
		`<?xml version="1.0"?><i><d p="0.5,1,16777215,user1">弹幕Strm命中</d></i>`,
		&seen)
	overrideDanmakuOfficialBase(t, official.URL)

	svc := newDanmakuTestService(t)
	var gotProvider string
	svc.SetStrmResolver(func(_ context.Context, provider string, _ url.Values) (*StrmPlayResult, error) {
		gotProvider = provider
		// 播放链路 302 直链 + 绑定的 UA 头（防服务端直连 403）。
		return &StrmPlayResult{
			RedirectURL: rangeSrv.URL,
			Link:        &cloud.DirectLink{URL: rangeSrv.URL, Headers: map[string]string{"User-Agent": "bound-ua-115"}},
		}, nil
	})
	ctx := context.Background()
	strmPath := filepath.Join(t.TempDir(), "远程动画.第01话.strm")
	require.NoError(t, os.WriteFile(strmPath, []byte("http://example.invalid/api/strm/play/115/video.mkv?acct=1&pickcode=abc\n"), 0o644))
	seedDanmakuVideoMedia(t, svc, "mS", "远程动画", strmPath, 64, 0)
	// STRMURL 需要显式写回（扫库时才解析）。
	var media model.Media
	require.NoError(t, svc.repo.DB.First(&media, "id = ?", "mS").Error)
	media.STRMURL = "/api/strm/play/115/video.mkv?acct=1&pickcode=abc"
	require.NoError(t, svc.repo.DB.Save(&media).Error)

	res, err := svc.Fetch(ctx, "mS", "", "")
	require.NoError(t, err)
	require.True(t, res.Enabled)
	require.Contains(t, res.Raw, "弹幕Strm命中")
	require.Equal(t, "115", gotProvider)
	require.Contains(t, gotRange, "bytes=0-")
	require.Equal(t, "bound-ua-115", gotUA) // 防盗链 UA 必须透传
	require.Contains(t, seen, `"fileHash":"`+wantHash+`"`)
	require.Contains(t, seen, `"fileName":"`+url.QueryEscape("远程动画.第01话")+`"`)
	// strm 的 SizeBytes 是文本大小，不参与 match。
	require.Contains(t, seen, `"fileSize":0`)
}

// match 接口 fileName 语义：去扩展名；strm 文件名含视频扩展名时剥两层。
func TestDanmakuMatchFileName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/lib/某番剧.第01话.mkv", "某番剧.第01话"},
		{"/lib/某番剧.第01话.strm", "某番剧.第01话"},
		{"/lib/movie.mkv.strm", "movie"},
		{"plain", "plain"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, danmakuMatchFileName(c.in), "path=%s", c.in)
	}
}

// hashLocalFile：本地视频直接读盘算前 16MB MD5，且第二次走缓存。
func TestDanmakuHashLocalFile(t *testing.T) {
	videoPath, wantHash := writeDanmakuTestVideo(t, "hashme.mkv")
	svc := newDanmakuTestService(t)
	got, ok := svc.hashLocalFile(videoPath)
	require.True(t, ok)
	require.Equal(t, wantHash, got)
	got2, ok := svc.hashLocalFile(videoPath)
	require.True(t, ok)
	require.Equal(t, wantHash, got2)
	missing, ok := svc.hashLocalFile(filepath.Join(t.TempDir(), "nope.mkv"))
	require.False(t, ok)
	require.Empty(t, missing)
}

// 配置源与官方同源时，回退不重复请求同一台服务器（bases 只含一份）。
func TestDanmakuSameBase(t *testing.T) {
	require.True(t, sameDanmakuBase("https://api.dandanplay.net", "https://api.dandanplay.net"))
	require.False(t, sameDanmakuBase("https://api.dandanplay.net", "https://dm.example.com"))
	require.False(t, sameDanmakuBase("", "https://api.dandanplay.net"))
}

// fetchCommentWithFallback：配置源与官方同源时不重复请求；
// 全失败时带出最后一跳错误。
func TestDanmakuFetchCommentWithFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	svc := newDanmakuTestService(t)
	ctx := context.Background()
	raw, st, err := svc.fetchCommentWithFallback(ctx, srv.URL, srv.URL, "25484")
	require.Error(t, err)
	require.Empty(t, raw)
	require.Equal(t, "auto", st)
}

// 视频即便能命中 Hash 自动识别，当用户传入手动搜索关键词时应跳过 Hash 匹配，走关键词搜索。
func TestDanmakuFetchHashMatchSkippedOnManualKeyword(t *testing.T) {
	videoPath, _ := writeDanmakuTestVideo(t, "测试动画.第01话.mkv")

	// 官方服务同时提供 match 和 search：
	// match 会返回 episodeId=25484（动画A）
	// search 会根据关键词返回 episodeId=99999（动画B）
	mux := http.NewServeMux()
	var matchCalled bool
	mux.HandleFunc("/api/v2/match", func(w http.ResponseWriter, r *http.Request) {
		matchCalled = true
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"success":true,"isMatched":true,"matches":[{"episodeId":25484,"animeId":1001,"animeTitle":"自动识别动画A","episodeTitle":"第1话"}]}`)
	})
	mux.HandleFunc("/api/v2/search/episodes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"hasMore":false,"animes":[{"animeId":2002,"animeTitle":"手动搜索动画B","episodes":[{"episodeId":99999,"episodeTitle":"第1话"}]}]}`)
	})
	mux.HandleFunc("/api/v2/comment/25484", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0"?><i><d p="0.5,1,16777215,user1">自动识别弹幕</d></i>`)
	})
	mux.HandleFunc("/api/v2/comment/99999", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0"?><i><d p="0.5,1,16777215,user2">手动搜索弹幕</d></i>`)
	})
	official := httptest.NewServer(mux)
	t.Cleanup(official.Close)
	overrideDanmakuOfficialBase(t, official.URL)

	svc := newDanmakuTestService(t)
	ctx := context.Background()
	seedDanmakuVideoMedia(t, svc, "mManual", "自动识别动画A", videoPath, 32000, 1)

	// 1) 默认自动识别：命中 Hash 识别
	resAuto, err := svc.Fetch(ctx, "mManual", "", "")
	require.NoError(t, err)
	require.True(t, matchCalled)
	require.Equal(t, "hash", resAuto.MatchMode)
	require.Equal(t, int64(25484), resAuto.EpisodeID)
	require.Contains(t, resAuto.Raw, "自动识别弹幕")

	// 2) 用户传入手动搜索关键词：跳过 Hash 识别，命中搜索结果动画B
	resManual, err := svc.Fetch(ctx, "mManual", "手动搜索动画B", "")
	require.NoError(t, err)
	require.Equal(t, "search", resManual.MatchMode)
	require.Equal(t, int64(99999), resManual.EpisodeID)
	require.Equal(t, "手动搜索动画B", resManual.AnimeTitle)
	require.Contains(t, resManual.Raw, "手动搜索弹幕")
}

// Emby 远程挂载条目：通过伪装 ID 解析出流直链，通过 Range 提取 16MB 前缀计算 hash 并匹配弹幕。
func TestDanmakuFetchEmbyRemoteHashViaDirectLink(t *testing.T) {
	content := bytes.Repeat([]byte("emby-remote-video-bytes-9876543210"), 300)
	sum := md5.Sum(content)
	wantHash := hex.EncodeToString(sum[:])

	var gotRange string
	var rangeHits int
	rangeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHits++
		gotRange = r.Header.Get("Range")
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(content)
	}))
	t.Cleanup(rangeSrv.Close)

	var seen string
	official := danmakuOfficialServer(t,
		`{"success":true,"isMatched":true,"matches":[{"episodeId":25484,"animeId":2001,"animeTitle":"芙莉莲","episodeTitle":"第1话"}]}`,
		`<?xml version="1.0"?><i><d p="1.2,1,16777215,user1">Emby远程弹幕命中</d></i>`,
		&seen)
	overrideDanmakuOfficialBase(t, official.URL)

	remoteMediaID := EncodeEmbyRemoteID("mount-123", "remote-item-456")
	svc := newDanmakuTestService(t)
	svc.SetRemoteMediaResolver(func(_ context.Context, encodedID string) (*model.Media, string, error) {
		require.Equal(t, remoteMediaID, encodedID)
		return &model.Media{
			Base:         model.Base{ID: remoteMediaID},
			Title:        "葬送的芙莉莲",
			EpisodeTitle: "第1话",
			EpisodeNum:   1,
			Path:         "/mnt/emby/anime/Frieren/S01E01.mkv",
			SizeBytes:    int64(len(content)),
			DurationSec:  1400,
		}, rangeSrv.URL, nil
	})

	ctx := context.Background()
	res, err := svc.Fetch(ctx, remoteMediaID, "", "")
	require.NoError(t, err)
	require.True(t, res.Enabled)
	require.Equal(t, "hash", res.MatchMode)
	require.Equal(t, int64(25484), res.EpisodeID)
	require.Equal(t, "芙莉莲", res.AnimeTitle)
	require.Contains(t, res.Raw, "Emby远程弹幕命中")
	require.Contains(t, gotRange, "bytes=0-")
	require.Contains(t, seen, `"fileHash":"`+wantHash+`"`)
	require.Contains(t, seen, `"fileName":"`+url.QueryEscape("S01E01")+`"`)
	require.Equal(t, 1, rangeHits)

	// 第二次拉取验证 hashCache 命中，不重复请求 rangeSrv
	res2, err := svc.Fetch(ctx, remoteMediaID, "", "")
	require.NoError(t, err)
	require.Equal(t, "hash", res2.MatchMode)
	require.Equal(t, 1, rangeHits)
}

// Emby 远程直链拉取失败时（如网络异常），能平滑降级走番剧原名/标题关键词搜索。
func TestDanmakuFetchEmbyRemoteStreamFailedFallsBackToSearch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/search/episodes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 文件名搜索 ep01 时无结果，模拟文件名未匹配
		if r.URL.Query().Get("anime") == "ep01" {
			fmt.Fprint(w, `{"hasMore":false,"animes":[]}`)
			return
		}
		// 降级到番剧名搜索命中
		fmt.Fprint(w, `{"hasMore":false,"animes":[{"animeId":3001,"animeTitle":"降级搜索番剧","episodes":[{"episodeId":7799,"episodeTitle":"第1话"}]}]}`)
	})
	mux.HandleFunc("/api/v2/comment/7799", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0"?><i><d p="0.8,1,16777215,user1">降级搜索弹幕</d></i>`)
	})
	official := httptest.NewServer(mux)
	t.Cleanup(official.Close)
	overrideDanmakuOfficialBase(t, official.URL)

	remoteMediaID := EncodeEmbyRemoteID("mount-123", "remote-item-789")
	svc := newDanmakuTestService(t)
	// 返回一个不存在的流服务地址模拟 Range 拉取失败
	svc.SetRemoteMediaResolver(func(_ context.Context, encodedID string) (*model.Media, string, error) {
		return &model.Media{
			Base:        model.Base{ID: remoteMediaID},
			Title:       "降级搜索番剧",
			EpisodeNum:  1,
			Path:        "/mnt/emby/anime/fallback/ep01.mkv",
			DurationSec: 1200,
		}, "http://127.0.0.1:1/invalid-stream", nil
	})

	ctx := context.Background()
	res, err := svc.Fetch(ctx, remoteMediaID, "", "")
	require.NoError(t, err)
	require.True(t, res.Enabled)
	require.Equal(t, "search", res.MatchMode)
	require.Equal(t, int64(7799), res.EpisodeID)
	require.Equal(t, "降级搜索番剧", res.AnimeTitle)
	require.Contains(t, res.Raw, "降级搜索弹幕")
}
