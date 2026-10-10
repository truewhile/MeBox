package reader

import (
	"strings"
	"testing"
)

// 图片解密（coverDecodeJs / ruleContent.imageDecode）与 HLS 判定链路。

// 正文图片解密：书源规则把字节异或 0x55 后返回，服务端解密应还原原文。
func TestDecodeImageBytesRunsRuleJS(t *testing.T) {
	svc, srv, book := prepareCacheTestBook(t)
	ctx := t.Context()

	// 动态给书源加上 imageDecode 规则：XOR 0x55 还原。
	raw := strings.Replace(
		cacheTestSourceJSON(srv.URL),
		`"ruleContent": {"content": "id.content@textNodes"}`,
		`"ruleContent": {"content": "id.content@textNodes", "imageDecode": "var src = new Uint8Array(result); var out = new Uint8Array(src.length); for (var i=0;i<src.length;i++){ out[i] = src[i] ^ 0x55; } out"}`,
		1,
	)
	if _, err := svc.ImportSources(ctx, raw); err != nil {
		t.Fatalf("更新书源失败: %v", err)
	}
	rule := svc.ImageDecodeRule(ctx, book)
	if rule == "" {
		t.Fatal("imageDecode 规则未解析")
	}

	plain := []byte("PNG-PLAIN-BYTES")
	encoded := make([]byte, len(plain))
	for i, b := range plain {
		encoded[i] = b ^ 0x55
	}
	decoded, err := svc.DecodeImageBytes(ctx, book, "https://img.example.com/a.png", false, encoded)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if string(decoded) != string(plain) {
		t.Fatalf("解密结果 = %q，期望 %q", decoded, plain)
	}
}

// 没有规则时零开销直通（返回原始字节）。
func TestDecodeImageBytesPassthroughWithoutRule(t *testing.T) {
	svc, srv, book := prepareCacheTestBook(t)
	data := []byte("raw-image-bytes")
	out, err := svc.DecodeImageBytes(t.Context(), book, srv.URL+"/a.png", false, data)
	if err != nil {
		t.Fatalf("直通路径不应报错: %v", err)
	}
	if string(out) != string(data) {
		t.Fatalf("无规则时应原样返回: %q", out)
	}
}

// 解密脚本抛错时退回原始字节（保证至少还能看到图）。
func TestDecodeImageBytesFallsBackOnBadRule(t *testing.T) {
	svc, srv, book := prepareCacheTestBook(t)
	ctx := t.Context()
	raw := strings.Replace(
		cacheTestSourceJSON(srv.URL),
		`"ruleContent": {"content": "id.content@textNodes"}`,
		`"ruleContent": {"content": "id.content@textNodes", "imageDecode": "throw new Error('bad rule')"}`,
		1,
	)
	if _, err := svc.ImportSources(ctx, raw); err != nil {
		t.Fatalf("更新书源失败: %v", err)
	}
	data := []byte("still-an-image")
	out, err := svc.DecodeImageBytes(ctx, book, srv.URL+"/a.png", false, data)
	if err != nil {
		t.Fatalf("坏规则应降级而不是报错: %v", err)
	}
	if string(out) != string(data) {
		t.Fatalf("坏规则应返回原始字节: %q", out)
	}
}

// 封面解密：按书源 URL（搜索结果尚未入库）执行 coverDecodeJs。
func TestDecodeCoverBytes(t *testing.T) {
	svc, srv, _ := prepareCacheTestBook(t)
	ctx := t.Context()
	raw := strings.Replace(
		cacheTestSourceJSON(srv.URL),
		`"bookSourceType": 0,`,
		`"bookSourceType": 0, "coverDecodeJs": "var src = new Uint8Array(result); var out = new Uint8Array(src.length); for (var i=0;i<src.length;i++){ out[i] = src[i] ^ 0x33; } out",`,
		1,
	)
	if _, err := svc.ImportSources(ctx, raw); err != nil {
		t.Fatalf("更新书源失败: %v", err)
	}
	if !svc.SourceHasCoverDecode(srv.URL) {
		t.Fatal("应识别出该源声明了封面解密")
	}
	plain := []byte("cover-bytes")
	encoded := make([]byte, len(plain))
	for i, b := range plain {
		encoded[i] = b ^ 0x33
	}
	out := svc.DecodeCoverBytes(ctx, srv.URL, srv.URL+"/cover.jpg", encoded)
	if string(out) != string(plain) {
		t.Fatalf("封面解密结果 = %q，期望 %q", out, plain)
	}

	// 未声明解密的源：封面地址保持原样（不签代理）。
	plainURL := svc.RewriteBookCover(ctx, "", srv.URL+"/x", "https://img.example.com/c.jpg")
	if !strings.HasPrefix(plainURL, "https://img.example.com/") {
		t.Fatalf("无解密规则时不应改写封面: %q", plainURL)
	}
	// 声明了解密的源：封面走签名代理。
	signed := svc.RewriteBookCover(ctx, "", srv.URL, "https://img.example.com/c.jpg")
	if !strings.Contains(signed, "/api/reader/media?") || !strings.Contains(signed, "d=cover") {
		t.Fatalf("封面应走解密代理: %q", signed)
	}
}

// HLS 播放列表嗅探：#EXTM3U 标记优先于 Content-Type。
func TestReaderBodyIsPlaylist(t *testing.T) {
	playlist := []byte("#EXTM3U\n#EXT-X-VERSION:3\n")
	if !BodyIsPlaylist(playlist, "application/octet-stream") {
		t.Fatal("以 #EXTM3U 开头应判为播放列表")
	}
	if BodyIsPlaylist([]byte("\x89PNG\r\n"), "image/png") {
		t.Fatal("二进制图片不应判为播放列表")
	}
	if !BodyIsPlaylist([]byte("\n#EXTM3U\n"), "application/vnd.apple.mpegurl") {
		t.Fatal("带前置空行且 Content-Type 为 mpegurl 时应判为播放列表")
	}
	if BodyIsPlaylist(nil, "application/vnd.apple.mpegurl") {
		t.Fatal("空体不应判为播放列表")
	}
}
