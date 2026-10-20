package rule

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

	"github.com/dop251/goja"
)

// 本文件：书源 JS 的宿主浏览器能力契约。
//
// legado 用内置 WebView 实现 java.startBrowser / java.startBrowserAwait：
// 打开一个页面让用户完成防爬校验、登录或参数选择，并（Await 版本）把用户
// 操作后的页面源码作为 StrResponse 返回给书源解析。真实书源把整套「线路
// 切换」「用户后台」「书源设置」都建在这两个函数上，因此服务端必须提供
// 等价能力，否则这些按钮只会走到书源自己的 catch 分支。
//
// MeBox 没有 WebView，改由服务层把页面交给前端网页承载：
//   - AwaitBrowser：阻塞等待前端回传 DOM（对应 startBrowserAwait）
//   - OpenBrowser： 只展示不等待（对应 startBrowser / showBrowser）

// BrowserTask 一个需要用户在前端页面中完成的请求。
type BrowserTask struct {
	// URL 书源传入的原始地址，可能是 http(s)，也可能是 data:text/html;base64,…。
	URL string
	// Title 页面标题（对应 legado 对话框标题）。
	Title string
	// HTML URL 为 data: 系地址时解析出的页面源码，空表示由服务层按 URL 抓取。
	HTML string
	// Refetch 对应 legado startBrowserAwait 的 refetchAfterSuccess：
	// 用户确认后重新抓取该 URL 作为返回内容，而不是回传页面 DOM。
	Refetch bool
}

// BrowserResult 用户在页面上完成操作后的回传。
type BrowserResult struct {
	// URL 最终地址（对应 StrResponse.url()）。
	URL string
	// Body 页面内容：默认是用户操作后的 DOM 序列化，
	// Refetch 为真时是服务端重新抓取的响应体。
	Body string
}

// BrowserHost 由服务层注入的宿主浏览器实现。
type BrowserHost interface {
	// AwaitBrowser 展示页面并阻塞等待用户回传（java.startBrowserAwait）。
	AwaitBrowser(ctx context.Context, req BrowserTask) (BrowserResult, error)
	// OpenBrowser 展示页面但不等待（java.startBrowser / java.showBrowser）。
	OpenBrowser(ctx context.Context, req BrowserTask) error
}

// ErrBrowserCancelled 用户在页面里主动取消。
var ErrBrowserCancelled = errBrowserCancelled{}

type errBrowserCancelled struct{}

func (errBrowserCancelled) Error() string { return "用户取消了页面操作" }

// ─── 运行时侧：把页面交给宿主浏览器 ────────────────────────────────────────

// parseBrowserArgs 解析 startBrowser / startBrowserAwait 的位置参数。
//
//	startBrowser(url, title[, html])
//	startBrowserAwait(url, title[, refetchAfterSuccess][, html])
//
// 两者第 3 参类型不同（html 字符串 vs boolean），按实际类型判定。
func parseBrowserArgs(call goja.FunctionCall) BrowserTask {
	req := BrowserTask{
		URL:   stringArg(call, 0),
		Title: stringArgOr(call, 1, ""),
	}
	req.HTML = ParseDataHTML(req.URL)
	// 第 3 / 第 4 参里的字符串按 html 处理（data: URL 之外显式传入的页面源码）。
	for _, i := range []int{2, 3} {
		if len(call.Arguments) <= i {
			continue
		}
		arg := call.Arguments[i]
		if goja.IsUndefined(arg) || goja.IsNull(arg) {
			continue
		}
		if s, ok := arg.Export().(string); ok && strings.TrimSpace(s) != "" {
			req.HTML = s
		}
	}
	return req
}

// openBrowser 展示页面但不等待。
//
// 注入了宿主浏览器时交给宿主（前端可交互的页面面板）；未注入时退化为
// 记录地址供前端代开，不中断书源逻辑。
func (r *JSRunner) openBrowser(req BrowserTask) error {
	if req.URL == "" && req.HTML == "" {
		return nil
	}
	if r.cfg.Browser != nil {
		// 宿主抓取待展示的页面同样是网络等待，不能吃掉 JS 执行预算
		// （与 awaitBrowser 同一套暂停机制，见 pauseTimeout 的说明）。
		resume := r.pauseTimeout()
		defer resume()
		return r.cfg.Browser.OpenBrowser(r.ctx(), req)
	}
	if r.state != nil {
		r.state.OpenBrowser(req.URL, req.Title)
	}
	return nil
}

// awaitBrowser 展示页面并阻塞等待用户完成。
//
// 等待期间暂停 JS 超时计时：用户可能要看很久，默认 10s 的执行预算不能
// 用在「等人」上。
func (r *JSRunner) awaitBrowser(req BrowserTask) (BrowserResult, error) {
	if req.URL == "" && req.HTML == "" {
		return BrowserResult{}, fmt.Errorf("startBrowserAwait: 缺少页面地址")
	}
	if r.cfg.Browser == nil {
		if r.state != nil {
			r.state.OpenBrowser(req.URL, req.Title)
		}
		return BrowserResult{}, fmt.Errorf("服务端未提供浏览器能力，需要人工操作的页面请手动打开")
	}
	resume := r.pauseTimeout()
	defer resume()
	return r.cfg.Browser.AwaitBrowser(r.ctx(), req)
}

// ctx 返回本次执行的上下文（未注入时用 Background）。
func (r *JSRunner) ctx() context.Context {
	if r.cfg.Ctx != nil {
		return r.cfg.Ctx
	}
	return context.Background()
}

// requestUIRefresh 请求宿主重新渲染登录表单（java.reLoginView / refreshExplore）。
func (r *JSRunner) requestUIRefresh() {
	if ui, ok := r.state.(UIState); ok {
		ui.RequestUIRefresh()
	}
}

// applyLoginData 把 java.upLoginData 传入的值合并进登录表单。
func (r *JSRunner) applyLoginData(args []goja.Value) {
	if len(args) == 0 || goja.IsUndefined(args[0]) || goja.IsNull(args[0]) {
		return
	}
	ui, ok := r.state.(UIState)
	if !ok {
		return
	}
	exported, ok := args[0].Export().(map[string]any)
	if !ok {
		return
	}
	data := make(map[string]string, len(exported))
	for k, v := range exported {
		data[k] = anyToStringish(v)
	}
	ui.ApplyLoginData(data)
}

func anyToStringish(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", v)
	}
}

// ParseDataHTML 解析 data:text/html 系地址，返回内嵌的页面源码。
// 支持 base64 与百分号编码两种形式；非 data 地址或解码失败返回空串。
func ParseDataHTML(rawURL string) string {
	s := strings.TrimSpace(rawURL)
	if !strings.HasPrefix(s, "data:") {
		return ""
	}
	comma := strings.Index(s, ",")
	if comma < 0 {
		return ""
	}
	meta := s[len("data:"):comma]
	payload := s[comma+1:]
	// MIME 限定在 text/*（书源也可能用 text/plain 塞 HTML）
	mime := meta
	if i := strings.Index(mime, ";"); i >= 0 {
		mime = mime[:i]
	}
	if mime != "" && !strings.HasPrefix(mime, "text/") {
		return ""
	}
	if strings.Contains(strings.ToLower(meta), ";base64") {
		// base64 可能带换行；同时兼容 URL-safe 变体
		cleaned := strings.Map(func(r rune) rune {
			switch r {
			case '\n', '\r', ' ', '\t':
				return -1
			}
			return r
		}, payload)
		if b, err := base64.StdEncoding.DecodeString(cleaned); err == nil {
			return string(b)
		}
		if b, err := base64.RawStdEncoding.DecodeString(cleaned); err == nil {
			return string(b)
		}
		if b, err := base64.URLEncoding.DecodeString(cleaned); err == nil {
			return string(b)
		}
		if b, err := base64.RawURLEncoding.DecodeString(cleaned); err == nil {
			return string(b)
		}
		return ""
	}
	if dec, err := url.PathUnescape(payload); err == nil {
		return dec
	}
	return ""
}
