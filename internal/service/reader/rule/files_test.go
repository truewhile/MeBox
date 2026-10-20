package rule

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// 本文件：java 文件接口（cacheFile / downloadFile / readTxtFile / deleteFile）
// 的语义回归。对应 legado JsExtensions：
//   - cacheFile(url) → 返回文件**文本内容**（不是路径）；
//   - downloadFile(url) → 返回相对缓存根目录的路径；
//   - readTxtFile(path[, charset]) → 返回文本；
//   - 只允许访问缓存目录内的文件。

func newFileTestRunner(cacheDir, baseURL string, hits *int32) *JSRunner {
	return NewJSRunner(JSConfig{
		CacheDir: cacheDir,
		BaseURL:  baseURL,
		Fetch: func(req *Request) (string, string, int, error) {
			if hits != nil {
				atomic.AddInt32(hits, 1)
			}
			resp, err := http.Get(req.URL) //nolint:gosec // 测试内固定 httptest 地址
			if err != nil {
				return "", "", 0, err
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			return string(b), req.URL, resp.StatusCode, nil
		},
	})
}

func TestCacheFileReturnsTextContent(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("第一章 开始\n第二章 继续"))
	}))
	defer srv.Close()

	r := newFileTestRunner(t.TempDir(), srv.URL, &hits)
	ar := NewAnalyzeRule()
	v, err := r.Run(ar, "java.cacheFile('"+srv.URL+"/vol.txt')", nil, "")
	if err != nil {
		t.Fatalf("cacheFile 失败: %v", err)
	}
	if got := anyToString(v); got != "第一章 开始\n第二章 继续" {
		t.Fatalf("cacheFile 应返回文本内容，实际 %q", got)
	}
	// 第二次应命中缓存，不再发起请求。
	if _, err := r.Run(ar, "java.cacheFile('"+srv.URL+"/vol.txt')", nil, ""); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("重复调用应命中缓存（请求次数=%d）", n)
	}
}

func TestCacheFileDecodesGBK(t *testing.T) {
	gbk, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("中文内容测试"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(gbk)
	}))
	defer srv.Close()

	r := newFileTestRunner(t.TempDir(), srv.URL, nil)
	v, err := r.Run(NewAnalyzeRule(), "java.cacheFile('"+srv.URL+"/gbk.txt')", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := anyToString(v); got != "中文内容测试" {
		t.Fatalf("GBK 文本未正确解码: %q", got)
	}
}

func TestDownloadFileThenReadTxtFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("正文内容"))
	}))
	defer srv.Close()

	cacheDir := t.TempDir()
	r := newFileTestRunner(cacheDir, srv.URL, nil)
	ar := NewAnalyzeRule()

	rawPath, err := r.Run(ar, "java.downloadFile('"+srv.URL+"/a.txt')", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	rel := anyToString(rawPath)
	if !strings.HasPrefix(rel, "/") || !strings.HasSuffix(rel, ".txt") {
		t.Fatalf("downloadFile 应返回相对缓存路径，实际 %q", rel)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "reader", "files", strings.TrimPrefix(rel, "/"))); err != nil {
		t.Fatalf("文件未落到缓存目录: %v", err)
	}

	v, err := r.Run(ar, "java.readTxtFile('"+rel+"')", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := anyToString(v); got != "正文内容" {
		t.Fatalf("readTxtFile = %q", got)
	}

	del, err := r.Run(ar, "String(java.deleteFile('"+rel+"'))", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if anyToString(del) != "true" {
		t.Fatalf("deleteFile 应返回 true，实际 %q", anyToString(del))
	}
}

func TestFileOpsRejectPathTraversal(t *testing.T) {
	cacheDir := t.TempDir()
	secret := filepath.Join(cacheDir, "secret.txt")
	if err := os.WriteFile(secret, []byte("不该被读到"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newFileTestRunner(cacheDir, "", nil)
	ar := NewAnalyzeRule()

	for _, p := range []string{"/../../secret.txt", "../secret.txt", "/../reader/files/../../secret.txt"} {
		v, err := r.Run(ar, "java.readTxtFile('"+p+"')", nil, "")
		if err != nil {
			t.Fatalf("%s 不应报错: %v", p, err)
		}
		if got := anyToString(v); strings.Contains(got, "不该被读到") {
			t.Fatalf("越权读到了缓存目录外的文件（%s）", p)
		}
	}
}

func TestFileOpsWithoutCacheDirFail(t *testing.T) {
	r := NewJSRunner(JSConfig{})
	_, err := r.Run(NewAnalyzeRule(), "java.cacheFile('https://example.com/a.txt')", nil, "")
	if err == nil || !strings.Contains(err.Error(), "缓存目录") {
		t.Fatalf("未配置缓存目录时应明确报错，实际 %v", err)
	}
}
