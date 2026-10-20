package reader

import (
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// 本文件：段落评论接口（/para_review）的分页适配。
//
// 上游聚合站自带的评论页按 page=N 翻页（滑到底请求 page=2、page=3…），但它的列表
// 接口早就改成了游标分页：响应里给 next_cursor，请求时要带 cursor=。接口不认识
// page，于是每次「加载更多」都原样返回第一页——一条新评论都加不进去，has_more 又
// 始终是 true，评论页就一遍遍触发加载更多、反复闪骨架屏。用户看到的就是「段评往
// 下滑一闪一闪」，其实评论一条没多。
//
// 评论页是上游 HTML，改不了，于是在代理层做最小适配：
//   - 请求带 page=N（N>1）且有上一页给的游标时，换成 cursor= 请求；
//   - 某页返回的 comment_id 全是已经见过的，就把 has_more 改成 false 收尾，
//     免得页面在「加载更多 → 没有新内容」之间空转。
//
// 只认路径以 /para_review 结尾、带 page 且不带 cursor 的请求；对不上就原样透传，
// 不影响别的书源。

// commentPageState 一个评论列表（同一段落 + 同一排序）的翻页状态。
type commentPageState struct {
	// cursor 上一页响应给的游标：下一页请求用它代替 page。
	cursor string
	// seen 已经下发给页面的 comment_id：整页重复时据此收尾。
	seen map[string]struct{}
}

// commentPager 承载页面里评论接口的翻页适配器（每个承载页面一份）。
type commentPager struct {
	mu     sync.Mutex
	states map[string]*commentPageState
}

func newCommentPager() *commentPager {
	return &commentPager{states: map[string]*commentPageState{}}
}

// rewriteRequest 按需把 page=N 改写成 cursor=<上一页游标>。
//
// 返回改写后的地址与状态键（空串表示这个请求不需要适配，调用方也不必观察响应）。
func (p *commentPager) rewriteRequest(target string) (string, string) {
	if p == nil {
		return target, ""
	}
	parsed, err := url.Parse(target)
	if err != nil || !isCommentListPath(parsed.Path) {
		return target, ""
	}
	query := parsed.Query()
	pageRaw := strings.TrimSpace(query.Get("page"))
	if pageRaw == "" || strings.TrimSpace(query.Get("cursor")) != "" {
		return target, ""
	}
	page, err := strconv.Atoi(pageRaw)
	if err != nil || page < 1 {
		return target, ""
	}
	key := commentPageKey(parsed.Path, query)
	if page == 1 {
		// 重新加载 / 换了排序：游标与去重表都从头来。
		p.mu.Lock()
		delete(p.states, key)
		p.mu.Unlock()
		return target, key
	}
	p.mu.Lock()
	cursor := ""
	if st := p.states[key]; st != nil {
		cursor = st.cursor
	}
	p.mu.Unlock()
	if cursor == "" {
		// 不是从第一页翻过来的（没有游标可用）：原样透传，仍然观察响应，
		// 靠下面的「整页重复」判断让页面停下来。
		return target, key
	}
	query.Set("cursor", cursor)
	query.Del("page")
	parsed.RawQuery = query.Encode()
	return parsed.String(), key
}

// observeResponse 观察评论列表响应：记下游标，并在整页重复时把 has_more 改成 false。
func (p *commentPager) observeResponse(key, body string) string {
	if p == nil || key == "" {
		return body
	}
	trimmed := strings.TrimSpace(body)
	if !strings.HasPrefix(trimmed, "{") {
		return body
	}
	var env commentListEnvelope
	if err := json.Unmarshal([]byte(trimmed), &env); err != nil {
		return body
	}
	if env.Data.Comments == nil && env.Data.HasMore == nil {
		return body
	}
	ids := make([]string, 0, len(env.Data.Comments))
	for _, c := range env.Data.Comments {
		if id := strings.TrimSpace(c.CommentID); id != "" {
			ids = append(ids, id)
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.states == nil {
		p.states = map[string]*commentPageState{}
	}
	st := p.states[key]
	if st == nil {
		st = &commentPageState{seen: map[string]struct{}{}}
		p.states[key] = st
	}
	repeated := len(ids) > 0
	for _, id := range ids {
		if _, ok := st.seen[id]; !ok {
			repeated = false
			break
		}
	}
	if repeated && env.Data.HasMore != nil && *env.Data.HasMore {
		// 整页都是老评论：接口不认 page，再翻下去也只会拿到同一页。
		// 改掉 has_more，让页面显示「没有更多了」，别再反复拉取闪骨架屏。
		if replaced, ok := setFirstJSONBoolFalse(trimmed, "has_more"); ok {
			return replaced
		}
		return body
	}
	for _, id := range ids {
		st.seen[id] = struct{}{}
	}
	if env.Data.HasMore != nil && !*env.Data.HasMore {
		// 已经是最后一页：游标链到头，清掉它，页面万一再要一页也别拿旧游标
		// 去请求（那种请求会落在下面的「整页重复」判断上收尾）。
		st.cursor = ""
		return body
	}
	// 游标只在拿到新内容时前进：请求失败/重复时页面会重试同一页，
	// 游标也跟着重试同一个。
	if cursor := rawJSONScalar(env.Data.Cursor); cursor != "" {
		st.cursor = cursor
	}
	return body
}

// commentListEnvelope 评论列表响应里我们关心的字段。
type commentListEnvelope struct {
	Data struct {
		Comments []struct {
			CommentID string `json:"comment_id"`
		} `json:"comments"`
		HasMore *bool           `json:"has_more"`
		Cursor  json.RawMessage `json:"next_cursor"`
	} `json:"data"`
}

// isCommentListPath 判断是否是段落评论列表接口（/para_review）。
func isCommentListPath(path string) bool {
	p := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(path)), "/")
	return strings.HasSuffix(p, "/para_review")
}

// commentPageKey 生成翻页状态键：接口路径 + 除 page/cursor 之外的查询参数。
// 同一段落的不同排序（sort_by）各自一套状态，互不干扰。
func commentPageKey(path string, query url.Values) string {
	rest := make(url.Values, len(query))
	for k, v := range query {
		if k == "page" || k == "cursor" {
			continue
		}
		rest[k] = v
	}
	keys := make([]string, 0, len(rest))
	for k := range rest {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(path)
	for _, k := range keys {
		for _, v := range rest[k] {
			b.WriteString("|" + k + "=" + v)
		}
	}
	return b.String()
}

// rawJSONScalar 取一个 JSON 标量的字面量（游标是毫秒时间戳，可能被上游写成数字）。
func rawJSONScalar(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	if strings.HasPrefix(s, `"`) {
		var out string
		if json.Unmarshal(raw, &out) == nil {
			return strings.TrimSpace(out)
		}
		return ""
	}
	return s
}

// setFirstJSONBoolFalse 把 JSON 里第一个 `"key":true` 改成 `"key":false`。
//
// 只做这一处等长替换，不重新序列化整份响应：评论里的 user_id、时间戳动辄十几位，
// 走一遍 float64 会变成科学计数法，页面再解析就废了。
func setFirstJSONBoolFalse(body, key string) (string, bool) {
	token := `"` + key + `"`
	idx := strings.Index(body, token)
	if idx < 0 {
		return body, false
	}
	i := idx + len(token)
	for i < len(body) && (body[i] == ' ' || body[i] == '\t' || body[i] == '\n' || body[i] == '\r') {
		i++
	}
	if i >= len(body) || body[i] != ':' {
		return body, false
	}
	i++
	for i < len(body) && (body[i] == ' ' || body[i] == '\t' || body[i] == '\n' || body[i] == '\r') {
		i++
	}
	if !strings.HasPrefix(body[i:], "true") {
		return body, false
	}
	return body[:i] + "false" + body[i+len("true"):], true
}
