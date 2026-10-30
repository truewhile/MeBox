package reader

import (
	"strings"
	"testing"
)

// 本文件：媒体直连（客户端自己取音频/漫画图片字节）的回归测试。
//
// 契约：
//   - 默认（ContentOptions 零值）必须与历史行为完全一致：媒体地址一律改写成
//     服务端签名代理，响应里不出现直连标记与请求头 —— 网页端依赖这一点。
//   - 客户端显式请求直连 + 书源开启开关时：保留上游原始地址，并下发取媒体
//     所需的请求头。
//   - 客户端请求直连但书源没开开关时：退回代理，不能把未开启的源推到客户端直连。

// TestMediaDirectDefaultsToProxy 不传直连选项时行为不变（网页端契约）。
func TestMediaDirectDefaultsToProxy(t *testing.T) {
	svc, _, book := prepareTicketBook(t)
	ctx := t.Context()

	out, err := svc.GetContentForBook(ctx, readerTestUserID, book.ID, 0, ContentOptions{})
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(out.Tracks) == 0 {
		t.Fatal("应至少有一条音轨")
	}
	if !strings.HasPrefix(out.Tracks[0], "/api/reader/media?") {
		t.Fatalf("默认应改写为服务端代理，实际 %q", out.Tracks[0])
	}
	if out.MediaDirect {
		t.Fatal("默认不应标记为直连")
	}
	if len(out.MediaHeaders) != 0 {
		t.Fatalf("代理模式不应下发取媒体请求头，实际 %v", out.MediaHeaders)
	}
}

// TestMediaDirectRequiresSourceSwitch 书源没开开关时，即使客户端请求直连也只给代理。
func TestMediaDirectRequiresSourceSwitch(t *testing.T) {
	svc, srv, book := prepareTicketBook(t)
	ctx := t.Context()

	out, err := svc.GetContentForBook(ctx, readerTestUserID, book.ID, 0, ContentOptions{MediaDirect: true})
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if out.MediaDirect {
		t.Fatal("书源未开启直连，不应标记为直连")
	}
	if !strings.HasPrefix(out.Tracks[0], "/api/reader/media?") {
		t.Fatalf("书源未开启直连时仍应走代理，实际 %q", out.Tracks[0])
	}
	_ = srv
}

// TestMediaDirectKeepsUpstreamURL 开启开关后：地址是上游原始地址，并下发请求头。
func TestMediaDirectKeepsUpstreamURL(t *testing.T) {
	svc, srv, book := prepareTicketBook(t)
	ctx := t.Context()

	if err := svc.SetSourceMediaDirect(ctx, readerTestUserID, sourceIDOf(t, svc, book.Origin), true); err != nil {
		t.Fatalf("开启直连失败: %v", err)
	}

	out, err := svc.GetContentForBook(ctx, readerTestUserID, book.ID, 0, ContentOptions{MediaDirect: true})
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !out.MediaDirect {
		t.Fatal("书源已开启直连，响应应标记 media_direct")
	}
	if len(out.Tracks) == 0 {
		t.Fatal("应至少有一条音轨")
	}
	track := out.Tracks[0]
	if strings.HasPrefix(track, "/api/") {
		t.Fatalf("直连模式不该返回服务端代理地址，实际 %q", track)
	}
	if !strings.HasPrefix(track, srv.URL+"/media") {
		t.Fatalf("直连模式应保留上游原始地址，实际 %q", track)
	}
	// 取媒体需要的防盗链头必须下发，否则客户端拿不到字节。
	if out.MediaHeaders["Referer"] == "" {
		t.Fatalf("直连模式应下发 Referer，实际 %v", out.MediaHeaders)
	}
	if out.MediaHeaders["User-Agent"] == "" {
		t.Fatalf("直连模式应下发 User-Agent，实际 %v", out.MediaHeaders)
	}
	// 传输层头交给客户端 HTTP 栈，不能下发。
	if _, ok := out.MediaHeaders["Accept-Encoding"]; ok {
		t.Fatalf("不应下发 Accept-Encoding: %v", out.MediaHeaders)
	}
}

// TestMediaDirectAppliesToCachedChapter 缓存命中的章节同样要按本次请求决定通道。
//
// 缓存里存的是书源侧原始地址（改写前），若命中后不改写就会把上游地址直接
// 漏给代理模式的客户端，表现为「图片/音频打不开」。
func TestMediaDirectAppliesToCachedChapter(t *testing.T) {
	svc, _, book := prepareTicketBook(t)
	ctx := t.Context()

	// 首次读取写入缓存（直连模式）。
	if err := svc.SetSourceMediaDirect(ctx, readerTestUserID, sourceIDOf(t, svc, book.Origin), true); err != nil {
		t.Fatalf("开启直连失败: %v", err)
	}
	first, err := svc.GetContentForBook(ctx, readerTestUserID, book.ID, 0, ContentOptions{MediaDirect: true})
	if err != nil || !first.MediaDirect {
		t.Fatalf("首次直连读取失败: %v", err)
	}

	// 同章再按代理模式读：必须拿到签名代理地址，而不是缓存里的上游地址。
	second, err := svc.GetContentForBook(ctx, readerTestUserID, book.ID, 0, ContentOptions{})
	if err != nil {
		t.Fatalf("二次读取失败: %v", err)
	}
	if second.MediaDirect {
		t.Fatal("代理模式不应标记直连")
	}
	if !strings.HasPrefix(second.Tracks[0], "/api/reader/media?") {
		t.Fatalf("缓存命中后仍应按代理改写，实际 %q", second.Tracks[0])
	}
}

// TestMediaDirectForceProxyWins 客户端显式要求代理时，即使书源开着直连也走代理。
//
// 这是直连取不到字节时的退路：不让用户为了继续读一章去改书源设置。
func TestMediaDirectForceProxyWins(t *testing.T) {
	svc, _, book := prepareTicketBook(t)
	ctx := t.Context()

	if err := svc.SetSourceMediaDirect(ctx, readerTestUserID, sourceIDOf(t, svc, book.Origin), true); err != nil {
		t.Fatalf("开启直连失败: %v", err)
	}

	out, err := svc.GetContentForBook(ctx, readerTestUserID, book.ID, 0,
		ContentOptions{MediaDirect: true, ForceProxy: true})
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if out.MediaDirect {
		t.Fatal("强制代理时不应标记直连")
	}
	if !strings.HasPrefix(out.Tracks[0], "/api/reader/media?") {
		t.Fatalf("强制代理时应返回签名代理地址，实际 %q", out.Tracks[0])
	}
	if len(out.MediaHeaders) != 0 {
		t.Fatalf("代理模式不应下发请求头，实际 %v", out.MediaHeaders)
	}
}

// sourceIDOf 按书源 URL 取当前用户的书源 ID。
func sourceIDOf(t *testing.T, svc *ReaderService, origin string) string {
	t.Helper()
	srcs, err := svc.ListSources(t.Context(), readerTestUserID)
	if err != nil {
		t.Fatalf("列书源失败: %v", err)
	}
	for _, src := range srcs {
		if src.SourceURL == origin {
			return src.ID
		}
	}
	t.Fatalf("找不到书源 %s", origin)
	return ""
}
