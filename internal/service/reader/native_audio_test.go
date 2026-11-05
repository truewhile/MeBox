package reader

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 原生解码与媒体通道独立：本地文件和 STRM 都保留原始目标及签名，不依赖 ffmpeg。
func TestNativeLocalAudioContent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string // 非空时写入 STRM；空则直接导入 WMA
	}{
		{name: "local WMA"},
		{name: "relative STRM WMA", target: "../original.wma"},
		{name: "remote STRM WMA", target: "https://cdn.example.com/01.wma?token=original"},
		{name: "STRM playback API WMA", target: "http://127.0.0.1:8080/api/strm/play/cloud115/video.wma?acct=a&pickcode=p"},
		{name: "remote STRM HLS", target: "https://cdn.example.com/01.m3u8?token=original"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newLocalBookService(t)
			svc.cfg.App.FFmpegPath = filepath.Join(t.TempDir(), "missing-ffmpeg")
			dir := t.TempDir()
			original := filepath.Join(dir, "01.wma")
			body := "original-wma-bytes"
			if tc.target != "" {
				original = tc.target
				body = tc.target + "\n"
			}
			name := "01.wma"
			if tc.target != "" {
				name = "01.strm"
			}
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o640); err != nil {
				t.Fatal(err)
			}
			if tc.target != "" && !isRemoteMediaURL(original) {
				original = filepath.Clean(filepath.Join(dir, original))
			}
			book, err := svc.ImportLocalAudioDir(t.Context(), "u1", dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, opts := range []ContentOptions{
				{NativeAudio: true},
				{NativeAudio: true, MediaDirect: true},
				{NativeAudio: true, ForceProxy: true},
			} {
				out, err := svc.GetContentForBook(t.Context(), "u1", book.ID, 0, opts)
				if err != nil {
					t.Fatalf("native opts=%+v: %v", opts, err)
				}
				if out.Type != "audio" || out.Transcoding || out.MediaDirect || len(out.Tracks) != 1 {
					t.Fatalf("native content = %+v", out)
				}
				if out.IsHLS != looksLikeHLSURL(original) {
					t.Fatalf("HLS flag=%v for %q", out.IsHLS, original)
				}
				track := out.Tracks[0]
				var got string
				if isRemoteMediaURL(original) {
					if !strings.HasPrefix(track, "/api/reader/media?") {
						t.Fatalf("remote target must use signed proxy: %q", track)
					}
					got, err = svc.VerifyProxyURL(book.ID, queryParam(t, track, "u"), queryParam(t, track, "s"))
					if _, badErr := svc.VerifyProxyURL(book.ID, queryParam(t, track, "u"), "bad"); badErr == nil {
						t.Fatal("tampered proxy signature accepted")
					}
				} else {
					if !strings.HasPrefix(track, "/api/reader/local/audio?") {
						t.Fatalf("local target must use signed audio endpoint: %q", track)
					}
					got, err = svc.VerifyLocalAudioURL(book.ID, queryParam(t, track, "p"), queryParam(t, track, "s"))
					if _, badErr := svc.VerifyLocalAudioURL(book.ID, queryParam(t, track, "p"), "bad"); badErr == nil {
						t.Fatal("tampered local signature accepted")
					}
				}
				if err != nil || got != original {
					t.Fatalf("original target = %q, err=%v, want %q", got, err, original)
				}
				batch, err := svc.GetContentBatch(t.Context(), "u1", book.ID, []int{0}, false, opts)
				if err != nil {
					t.Fatal(err)
				}
				if len(batch.Items) != 1 || batch.Items[0].Error != "" || batch.Items[0].Transcoding ||
					len(batch.Items[0].Tracks) != 1 || batch.Items[0].Tracks[0] != track || batch.Items[0].IsHLS != out.IsHLS {
					t.Fatalf("native batch = %+v", batch)
				}
			}
			if _, err := svc.GetContentForBook(t.Context(), "other", book.ID, 0, ContentOptions{NativeAudio: true}); err == nil {
				t.Fatal("native audio must retain book ownership check")
			}
			if !needsAudioTranscode(original) {
				return
			}
			// 网页默认及旧方法仍要求 ffmpeg。
			if _, err := svc.GetContentForBook(t.Context(), "u1", book.ID, 0, ContentOptions{}); !errors.Is(err, ErrAudioTranscodeUnavailable) {
				t.Fatalf("browser without ffmpeg: %v", err)
			}
			if _, err := svc.LocalChapterContent(t.Context(), "u1", book.ID, 0); !errors.Is(err, ErrAudioTranscodeUnavailable) {
				t.Fatalf("legacy local content without ffmpeg: %v", err)
			}
			// 这里只选择转码地址，不执行 ffmpeg；用已存在的可执行文件满足 LookPath。
			svc.cfg.App.FFmpegPath, err = os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			out, err := svc.GetContentForBook(t.Context(), "u1", book.ID, 0, ContentOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !out.Transcoding || len(out.Tracks) != 1 || out.Tracks[0] != svc.AudioTranscodeURL(book.ID, original) {
				t.Fatalf("browser must still select transcode: %+v", out)
			}
			batch, err := svc.GetContentBatch(t.Context(), "u1", book.ID, []int{0}, false, ContentOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(batch.Items) != 1 || batch.Items[0].Error != "" || !batch.Items[0].Transcoding {
				t.Fatalf("browser batch must report transcoding: %+v", batch)
			}
		})
	}
}
