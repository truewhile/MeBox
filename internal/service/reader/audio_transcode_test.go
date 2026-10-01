package reader

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/truewhile/MeBox/internal/model"
)

// TestAudioSourceExt 音频地址取扩展名：本地路径、带 query 的远端地址、
// Windows 盘符（会被 url.Parse 当成单字符 scheme）都要认对。
func TestAudioSourceExt(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`D:\media\斗破苍穹\001-250\001.wma`, ".wma"},
		{"/media/books/01.WMA", ".wma"},
		{"http://127.0.0.1:8080/api/strm/play/cloud115/video.wma?acct=x&path=y", ".wma"},
		{"https://cdn.example.com/a/b.mp3?token=1#frag", ".mp3"},
		{"https://cdn.example.com/stream", ""},
		{"/media/books/01.m4a", ".m4a"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := audioSourceExt(tc.in); got != tc.want {
			t.Errorf("audioSourceExt(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

// TestNeedsAudioTranscode 只对浏览器确定解不了的格式转码，其余维持直出。
func TestNeedsAudioTranscode(t *testing.T) {
	need := []string{
		`D:\media\001.wma`,
		"http://127.0.0.1:8080/api/strm/play/cloud115/video.wma?acct=x",
		"/media/01.asf",
		"/media/01.ape",
	}
	for _, s := range need {
		if !needsAudioTranscode(s) {
			t.Errorf("%q 应需要转码", s)
		}
	}
	direct := []string{
		"/media/01.mp3", "/media/01.m4a", "/media/01.m4b", "/media/01.flac",
		"/media/01.ogg", "/media/01.opus", "/media/01.wav",
		"https://cdn.example.com/stream", "",
	}
	for _, s := range direct {
		if needsAudioTranscode(s) {
			t.Errorf("%q 不该被转码", s)
		}
	}
}

// TestAudioTranscodeURLSignature 转码地址的签名可校验、改一处即失效。
func TestAudioTranscodeURLSignature(t *testing.T) {
	svc := newLocalBookService(t)
	source := `D:\media\斗破苍穹\001-250\001.wma`

	raw := svc.AudioTranscodeURL("book-1", source)
	if !strings.HasPrefix(raw, "/api/reader/audio/transcode?b=book-1&u=") {
		t.Fatalf("转码地址格式不对: %q", raw)
	}
	encoded, sig := queryParam(t, raw, "u"), queryParam(t, raw, "s")
	got, err := svc.VerifyAudioTranscodeURL("book-1", encoded, sig)
	if err != nil || got != source {
		t.Fatalf("签名校验失败: %v / %q", err, got)
	}
	if _, err := svc.VerifyAudioTranscodeURL("book-1", encoded, "deadbeef"); err == nil {
		t.Fatal("错误签名应校验失败")
	}
	if _, err := svc.VerifyAudioTranscodeURL("book-2", encoded, sig); err == nil {
		t.Fatal("换一本书后签名应失效")
	}
}

// TestAudioTranscodeCachePath 缓存名对（书 + 源地址）稳定且互不冲突。
func TestAudioTranscodeCachePath(t *testing.T) {
	dir := t.TempDir()
	a := audioTranscodeCachePath(dir, "book-1", "/x/001.wma")
	b := audioTranscodeCachePath(dir, "book-1", "/x/001.wma")
	if a != b {
		t.Fatalf("同一输入应得到同一缓存路径: %q vs %q", a, b)
	}
	if !strings.HasSuffix(a, ".mp3") {
		t.Fatalf("缓存文件应为 mp3: %q", a)
	}
	for _, other := range []string{
		audioTranscodeCachePath(dir, "book-2", "/x/001.wma"),
		audioTranscodeCachePath(dir, "book-1", "/x/002.wma"),
	} {
		if other == a {
			t.Fatalf("不同书/不同章节不该共用缓存: %q", other)
		}
	}
}

// TestEnsureTranscodedAudioWithoutFFmpeg 没装 ffmpeg 时要给出可照做的错误。
func TestEnsureTranscodedAudioWithoutFFmpeg(t *testing.T) {
	svc := newLocalBookService(t)
	svc.cfg.App.FFmpegPath = filepath.Join(t.TempDir(), "definitely-missing-ffmpeg")

	_, err := svc.EnsureTranscodedAudio(t.Context(), "book-1", `D:\media\001.wma`)
	if !errors.Is(err, ErrAudioTranscodeUnavailable) {
		t.Fatalf("err = %v，应包含 ErrAudioTranscodeUnavailable", err)
	}
	if !strings.Contains(err.Error(), "WMA") || !strings.Contains(err.Error(), "ffmpeg") {
		t.Fatalf("错误信息应说明格式与解决办法，实际 %q", err.Error())
	}
}

// TestEnsureTranscodedAudioUsesCache 已有转码结果时直接命中缓存，不依赖 ffmpeg。
func TestEnsureTranscodedAudioUsesCache(t *testing.T) {
	svc := newLocalBookService(t)
	svc.cfg.App.FFmpegPath = filepath.Join(t.TempDir(), "definitely-missing-ffmpeg")

	source := `D:\media\001.wma`
	dir, err := svc.audioTranscodeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := audioTranscodeCachePath(dir, "book-1", source)
	if err := os.WriteFile(want, []byte("MP3DATA"), 0o640); err != nil {
		t.Fatal(err)
	}

	got, err := svc.EnsureTranscodedAudio(t.Context(), "book-1", source)
	if err != nil {
		t.Fatalf("命中缓存时不该报错: %v", err)
	}
	if got != want {
		t.Fatalf("缓存路径 = %q，期望 %q", got, want)
	}
	data, err := os.ReadFile(got)
	if err != nil || string(data) != "MP3DATA" {
		t.Fatalf("缓存内容被改动了: %q / %v", data, err)
	}
}

// TestBuildFFmpegAudioArgs ffmpeg 参数：输入输出位置正确，丢掉视频流，
// 强制 mp3 编码，请求头按 key 排序保证可复现。
func TestBuildFFmpegAudioArgs(t *testing.T) {
	args := buildFFmpegAudioArgs("/in/01.wma", "/out/01.mp3", nil)
	if args[0] != "-hide_banner" || args[len(args)-1] != "/out/01.mp3" {
		t.Fatalf("参数首尾不对: %v", args)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-i /in/01.wma", "-vn", "-map 0:a:0", "-c:a libmp3lame", "-f mp3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("参数缺少 %q: %v", want, args)
		}
	}

	withHeaders := buildFFmpegAudioArgs("u", "o", map[string]string{
		"User-Agent": "mebox", "Cookie": "a=1",
	})
	idx := indexOf(withHeaders, "-headers")
	if idx < 0 {
		t.Fatalf("应带上 -headers: %v", withHeaders)
	}
	headers := withHeaders[idx+1]
	if !strings.HasPrefix(headers, "Cookie: a=1\r\n") || !strings.Contains(headers, "User-Agent: mebox\r\n") {
		t.Fatalf("请求头内容或顺序不对: %q", headers)
	}
}

// TestPruneAudioTranscodeCache 超过上限时按访问时间淘汰最旧的，落到 90%。
func TestPruneAudioTranscodeCache(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	sizes := []int64{400, 400, 400}
	names := []string{"old.mp3", "mid.mp3", "new.mp3"}
	var total int64
	for i, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, sizes[i]), 0o640); err != nil {
			t.Fatal(err)
		}
		ts := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(filepath.Join(dir, name), ts, ts); err != nil {
			t.Fatal(err)
		}
		total += sizes[i]
	}
	// 上限设为总量的一半：必须淘汰
	pruneAudioTranscodeCache(dir, total/2)

	if _, err := os.Stat(filepath.Join(dir, "old.mp3")); !os.IsNotExist(err) {
		t.Errorf("最旧的缓存应被删除，stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.mp3")); err != nil {
		t.Errorf("最新的缓存应保留: %v", err)
	}
}

// TestLocalAudioTrackWithoutFFmpeg localAudioTrack 在需要转码但没 ffmpeg 时，
// 直接给出可读的错误，而不是返回一个注定播不了的地址。
func TestLocalAudioTrackWithoutFFmpeg(t *testing.T) {
	svc := newLocalBookService(t)
	svc.cfg.App.FFmpegPath = filepath.Join(t.TempDir(), "definitely-missing-ffmpeg")

	book := &model.ReaderBook{Base: model.Base{ID: "book-1"}, Type: 1}
	ch := model.ReaderChapter{Index: 0, Tag: `D:\media\001.wma`}
	_, transcoding, err := svc.localAudioTrack(book, ch)
	if err == nil {
		t.Fatal("缺 ffmpeg 时应报错")
	}
	if transcoding {
		t.Error("报错时不该标记为转码中")
	}
	if !strings.Contains(err.Error(), "ffmpeg") {
		t.Fatalf("错误信息应提到 ffmpeg: %q", err.Error())
	}
}

// TestLocalAudioTrackPlayableStaysDirect 浏览器能播的格式不转码，仍走本地音频端点。
func TestLocalAudioTrackPlayableStaysDirect(t *testing.T) {
	svc := newLocalBookService(t)
	book := &model.ReaderBook{Base: model.Base{ID: "book-1"}, Type: 1}
	ch := model.ReaderChapter{Index: 0, Tag: `D:\media\001.mp3`}

	track, transcoding, err := svc.localAudioTrack(book, ch)
	if err != nil {
		t.Fatalf("mp3 不该报错: %v", err)
	}
	if transcoding {
		t.Error("mp3 不该标记为转码中")
	}
	if !strings.HasPrefix(track, "/api/reader/local/audio?") {
		t.Fatalf("mp3 应走本地音频端点: %q", track)
	}
}

// queryParam 取查询参数（签名用例共用）。
func queryParam(t *testing.T, raw, key string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get(key)
}

func indexOf(items []string, want string) int {
	for i, v := range items {
		if v == want {
			return i
		}
	}
	return -1
}
