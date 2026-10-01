package cloud115

import (
	"context"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
)

func TestFileSHA1(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := FileSHA1(path)
	if err != nil {
		t.Fatal(err)
	}
	// sha1("hello") = aaf4c61ddcc5e8a2dabede0f3b482cd9aea9434d，115 要求大写
	if sum != "AAF4C61DDCC5E8A2DABEDE0F3B482CD9AEA9434D" {
		t.Errorf("unexpected sha1: %s", sum)
	}
}

func TestFileSHA1Partial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.txt")
	// 10 bytes: "0123456789"
	if err := os.WriteFile(path, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	// bytes [2,4] = "234"
	sum, err := FileSHA1Partial(path, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if sum != "0EC09EF9836DA03F1ADD21E3EF607627E687E790" {
		t.Errorf("unexpected partial sha1: %s", sum)
	}
}

// TestFileSHA1PartialSmallerThanWindow 回归测试：经典 bug 是 io.CopyN 在文件不足
// length 字节时返回 io.EOF。115 上传固定用 [0,128*1024-1] 窗口计算 preid，导致所有
// 小于 128 KiB 的元数据文件（如海报/缩略图）上传必然失败。
func TestFileSHA1PartialSmallerThanWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "small.bin")
	// 6 字节小文件，不足 128 KiB 窗口
	if err := os.WriteFile(path, []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := FileSHA1Partial(path, 0, 128*1024-1)
	if err != nil {
		t.Fatalf("compute partial sha1 for small file should not fail: %v", err)
	}
	// 应等于整个文件（6 字节）的 sha1
	if sum != "1F8AC10F23C5B5BC1167BDA84B833E5C057A77D2" {
		t.Errorf("unexpected partial sha1: %s", sum)
	}
}

func TestParseSignCheckRange(t *testing.T) {
	rng, err := parseSignCheckRange("0-131071")
	if err != nil {
		t.Fatal(err)
	}
	if rng.Start != 0 || rng.End != 131071 {
		t.Errorf("unexpected range: %+v", rng)
	}
	if _, err := parseSignCheckRange("bad"); err == nil {
		t.Error("expected error for bad range")
	}
	if _, err := parseSignCheckRange("100-50"); err == nil {
		t.Error("expected error for end<start")
	}
}

func TestCalculateMultipartPartSize(t *testing.T) {
	// small file: 1 MiB -> partSize 32MiB, 1 part
	ps, parts, err := CalculateMultipartPartSize(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	if ps != defaultMultipartPartSize {
		t.Errorf("partSize=%d, want %d", ps, defaultMultipartPartSize)
	}
	if parts != 1 {
		t.Errorf("parts=%d, want 1", parts)
	}
	// zero-size -> 1 part
	_, parts, err = CalculateMultipartPartSize(0)
	if err != nil {
		t.Fatal(err)
	}
	if parts != 1 {
		t.Errorf("zero-size parts=%d, want 1", parts)
	}
	// negative -> error
	if _, _, err := CalculateMultipartPartSize(-1); err == nil {
		t.Error("expected error for negative size")
	}
}

func TestBaseNameOf(t *testing.T) {
	if got := baseNameOf("/a/b/file.nfo"); got != "file.nfo" {
		t.Errorf("got %s", got)
	}
	if got := baseNameOf("a\\b\\c.jpg"); got != "c.jpg" {
		t.Errorf("got %s", got)
	}
	if got := baseNameOf("top.txt"); got != "top.txt" {
		t.Errorf("got %s", got)
	}
}

// fakeCallbackOSSClient 捕获 CompleteMultipartUpload 收到的 callback / callback_var，
// 用于断言已经 Base64 编码（116 要求 callback 必须是 Base64 后的 JSON，否则报
// "The callback configuration is not base64 encoded"）。
type fakeCallbackOSSClient struct {
	capturedCallback    string
	capturedCallbackVar string
}

func (c *fakeCallbackOSSClient) InitiateMultipartUpload(_ context.Context, _ *oss.InitiateMultipartUploadRequest, _ ...func(*oss.Options)) (*oss.InitiateMultipartUploadResult, error) {
	return &oss.InitiateMultipartUploadResult{UploadId: oss.Ptr("upload-new")}, nil
}
func (c *fakeCallbackOSSClient) UploadPart(_ context.Context, r *oss.UploadPartRequest, _ ...func(*oss.Options)) (*oss.UploadPartResult, error) {
	if r.Body != nil {
		_, _ = io.Copy(io.Discard, r.Body)
	}
	return &oss.UploadPartResult{ETag: oss.Ptr("etag-1")}, nil
}
func (c *fakeCallbackOSSClient) ListParts(context.Context, *oss.ListPartsRequest, ...func(*oss.Options)) (*oss.ListPartsResult, error) {
	return &oss.ListPartsResult{}, nil
}
func (c *fakeCallbackOSSClient) CompleteMultipartUpload(_ context.Context, r *oss.CompleteMultipartUploadRequest, _ ...func(*oss.Options)) (*oss.CompleteMultipartUploadResult, error) {
	c.capturedCallback = *r.Callback
	c.capturedCallbackVar = *r.CallbackVar
	return &oss.CompleteMultipartUploadResult{
		CallbackResult: map[string]any{
			"state": true,
			"data":  map[string]any{"file_id": "file-1", "pick_code": "pick-1"},
		},
	}, nil
}
func (c *fakeCallbackOSSClient) AbortMultipartUpload(context.Context, *oss.AbortMultipartUploadRequest, ...func(*oss.Options)) (*oss.AbortMultipartUploadResult, error) {
	return &oss.AbortMultipartUploadResult{}, nil
}

// TestCompleteMultipartUploadCallbackBase64 回归测试：OSS CompleteMultipartUpload 的
// callback 必须 Base64 编码，否则报 "The callback configuration is not base64 encoded"，
// 导致大于 128 KiB 的元数据文件上传失败。
func TestCompleteMultipartUploadCallbackBase64(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.bin")
	data := make([]byte, 8) // 8 字节，PartSize=8 → 1 part
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeCallbackOSSClient{}
	uploader := &OSSMultipartUploader{client: fake}

	callback := `{"callbackUrl":"http://uplb.115.com/3.0/completeupload.php"}`
	callbackVar := `{"x:pick_code":"abc"}`
	_, err := uploader.UploadFileWithResult(context.Background(), OSSMultipartUploadInput{
		Bucket:      "bucket-1",
		Object:      "object-1",
		Callback:    callback,
		CallbackVar: callbackVar,
		FilePath:    path,
		FileSize:    int64(len(data)),
		PartSize:    8,
	})
	if err != nil {
		t.Fatalf("multipart 上传失败：%v", err)
	}
	// 捕获的 callback 必须是合法 Base64，且解码后与原 JSON 一致
	cbBytes, err := base64.StdEncoding.DecodeString(fake.capturedCallback)
	if err != nil {
		t.Fatalf("callback 未 Base64 编码：%v (raw=%q)", err, fake.capturedCallback)
	}
	if string(cbBytes) != callback {
		t.Errorf("callback 解码后 = %s，期望 %s", cbBytes, callback)
	}
	cbvBytes, err := base64.StdEncoding.DecodeString(fake.capturedCallbackVar)
	if err != nil {
		t.Fatalf("callback_var 未 Base64 编码：%v (raw=%q)", err, fake.capturedCallbackVar)
	}
	if string(cbvBytes) != callbackVar {
		t.Errorf("callback_var 解码后 = %s，期望 %s", cbvBytes, callbackVar)
	}
}
