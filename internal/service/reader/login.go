package reader

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/truewhile/MeBox/internal/service/reader/rule"
)

// 本文件：书源登录（对应 legado SourceLoginDialog / SourceLoginViewModel）。
//
// legado 的登录模型：
//   - loginUrl 是一段 JS，既是登录逻辑，也是 loginUi 各按钮 action 的函数库；
//   - loginUi 是一份表单描述（RowUi 数组：text / password / button / toggle / select）；
//   - 点按钮时执行 "loginUrl + '\n' + action"，作用域里 result 是"表单当前值"，
//     返回值被丢弃，只有抛异常才算失败；
//   - 点"登录"确认按钮时执行 loginUrl 里的 login() 函数（不存在则报错）。
//
// 服务端无法弹窗，因此把 java.toast / java.startBrowser 的调用收集起来回传前端。

// LoginField 登录表单的一个控件（对应 legado RowUi）。
type LoginField struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"` // text / password / button / toggle / select
	Action  string   `json:"action,omitempty"`
	Chars   []string `json:"chars,omitempty"`
	Default string   `json:"default,omitempty"`
	// ViewName 按钮/标签文案；legado 中它本身可以是 JS 表达式。
	ViewName string         `json:"viewName,omitempty"`
	Style    map[string]any `json:"style,omitempty"`
}

// SourceLoginInfo 书源登录界面描述与当前状态。
type SourceLoginInfo struct {
	SourceID   string       `json:"source_id"`
	SourceName string       `json:"source_name"`
	// HasLoginJS loginUrl 提供了登录逻辑。
	HasLoginJS bool `json:"has_login_js"`
	// LoginJS 登录逻辑 JS 原文（前端只作展示/调试，不执行）。
	LoginJS string `json:"login_js,omitempty"`
	// Fields 登录表单控件。
	Fields []LoginField `json:"fields"`
	// Values 已保存的登录信息（表单回填）。
	Values map[string]string `json:"values"`
	// Cookies 当前已保存的 Cookie（domain → cookie 串）。
	Cookies map[string]string `json:"cookies"`
	// Variable 源变量 JSON 原文（供变量编辑器）。
	Variable string `json:"variable"`
	// VariableComment 源变量说明（书源作者写的填写指引）。
	VariableComment string `json:"variable_comment,omitempty"`
	// LoggedIn 是否已具备登录态（存了登录信息或 Cookie）。
	LoggedIn bool `json:"logged_in"`
	// LoginFieldsHint loginUi 解析或执行的失败原因（不阻断展示）。
	Error string `json:"error,omitempty"`
}

// LoginResult 登录动作执行结果。
type LoginResult struct {
	OK       bool                  `json:"ok"`
	Error    string                `json:"error,omitempty"`
	Toasts   []string              `json:"toasts,omitempty"`
	Browsers []rule.BrowserRequest `json:"browsers,omitempty"`
	// UIRefresh 书源通过 java.reLoginView / refreshExplore / upLoginData
	// 要求重新渲染登录表单（前端据此重建 loginUi）。
	UIRefresh bool `json:"ui_refresh,omitempty"`
	// Values 执行后的登录信息（可能与执行前不同，如 checkStatus 回填邮箱）。
	Values   map[string]string `json:"values"`
	Cookies  map[string]string `json:"cookies"`
	LoggedIn bool              `json:"logged_in"`
}

// GetSourceLogin 返回书源登录界面描述与当前登录状态。
func (s *ReaderService) GetSourceLogin(ctx context.Context, userID, sourceID string) (*SourceLoginInfo, error) {
	src, bs, err := s.loadSource(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	sess := s.newSession(ctx, src, bs)
	sess.userID = userID
	defer sess.close()
	state := sess.state

	info := &SourceLoginInfo{
		SourceID:        src.ID,
		SourceName:      firstNonEmpty(src.Name, bs.BookSourceName),
		HasLoginJS:      strings.TrimSpace(SPtr(bs.LoginURL)) != "",
		LoginJS:         bs.LoginJS(),
		Fields:          []LoginField{},
		Values:          map[string]string{},
		Cookies:         state.snapshotCookies(),
		Variable:        state.GetVariable(),
		VariableComment: SPtr(bs.VariableComment),
	}
	if m, err := parseLoginInfoValues(state.GetLoginInfo()); err == nil {
		info.Values = m
	}
	// 登录态以「是否拿到凭证（Cookie）」为准：
	// 只填过表单并不等于已登录，否则失败的登录也会显示为已登录。
	info.LoggedIn = len(info.Cookies) > 0

	fields, err := s.resolveLoginFields(sess, bs, info.Values)
	if err != nil {
		info.Error = err.Error()
		// loginUi 解析失败时若只有 loginUrl，仍可用「打开登录页」方式登录。
		if SPtr(bs.LoginUI) == "" {
			info.Error = ""
		}
	} else {
		info.Fields = fields
	}
	return info, nil
}

// resolveLoginFields 解析 loginUi：直接是 JSON 数组时直接解析；
// 是 @js:/<js> 时先执行得到 JSON（对应 legado evalUiJs）。
// values 为已保存的登录信息，作为 result 注入（legado 的 loginUi JS 会读它）。
func (s *ReaderService) resolveLoginFields(sess *sourceSession, bs *BookSource, values map[string]string) ([]LoginField, error) {
	raw := strings.TrimSpace(SPtr(bs.LoginUI))
	if raw == "" {
		return nil, fmt.Errorf("书源未配置登录界面（loginUi）")
	}
	if isJSWrapped(raw) {
		runner := sess.runner("", 0)
		v, err := runner.EvalAction(bs.LoginJS()+"\n"+stripJSWrapper(raw), loginBindings(sess, values))
		if err != nil {
			return nil, fmt.Errorf("loginUi JS 执行失败: %w", err)
		}
		raw = strings.TrimSpace(anyToStr(v))
	}
	var fields []LoginField
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, fmt.Errorf("loginUi 不是合法的 JSON 表单: %w", err)
	}
	return fields, nil
}

// RunLoginAction 执行一个登录动作。
//
// action 为 loginUi 里某个控件的 action（如 "login(true)" / "checkStatus()"）；
// fields 为前端提交的表单值，会与已保存的登录信息合并后作为 result 传入。
// action 为空时执行 loginUrl 里的 login()（即 legado 的「确认登录」）。
func (s *ReaderService) RunLoginAction(ctx context.Context, userID, sourceID, action string, fields map[string]string) (*LoginResult, error) {
	src, bs, err := s.loadSource(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	sess := s.newSession(ctx, src, bs)
	sess.userID = userID
	// 登录动作里才注入宿主浏览器：书源的「切换线路」「用户后台」等按钮
	// 依赖 java.startBrowserAwait 打开页面并等待用户操作。
	sess.browserEnabled = true
	defer sess.close()
	state := sess.state

	loginJS := bs.LoginJS()
	if loginJS == "" {
		return nil, fmt.Errorf("书源未配置登录逻辑（loginUrl）")
	}

	// 合并登录信息：已保存值 + 本次提交值（对应 legado getLoginData）。
	values := map[string]string{}
	if m, err := parseLoginInfoValues(state.GetLoginInfo()); err == nil {
		values = m
	}
	for k, v := range fields {
		values[k] = v
	}
	// 若提供了新的表单值，先持久化（legado 在调用登录函数前先存 loginInfo）。
	if len(fields) > 0 {
		if b, err := json.Marshal(values); err == nil {
			state.SetLoginInfo(string(b))
		}
	}

	body := loginJS + "\n"
	if strings.TrimSpace(action) == "" {
		// 对应 legado：login() 必须由书源实现
		body += "if (typeof login=='function'){ login.apply(this); } else { throw('Function login not implements!!!'); }"
	} else {
		body += action
	}

	runner := sess.runner("", 0)
	_, runErr := runner.EvalAction(body, loginBindings(sess, values))

	// 无论成功失败都要落库：Cookie/变量可能已被部分改写（如已拿到 token 但后续步骤报错）。
	state.flush()

	res := &LoginResult{
		OK:        runErr == nil,
		Toasts:    state.toasts,
		Browsers:  state.browsers,
		UIRefresh: state.UIRefreshRequested(),
		Values:    values,
		Cookies:   state.snapshotCookies(),
	}
	if runErr != nil {
		res.Error = runErr.Error()
	}
	// 登录态以是否拿到凭证（Cookie）为准，避免"只存了表单"被显示成已登录。
	res.LoggedIn = len(res.Cookies) > 0
	return res, nil
}

// SetSourceVariable 覆盖保存书源变量（前端变量编辑器）。
// 保存后书源 JS 的 getVariable() 即可读到。
func (s *ReaderService) SetSourceVariable(ctx context.Context, sourceID, variable string) error {
	if strings.TrimSpace(variable) != "" && !json.Valid([]byte(variable)) {
		return fmt.Errorf("变量必须是合法 JSON")
	}
	src, bs, err := s.loadSource(ctx, sourceID)
	if err != nil {
		return err
	}
	sess := s.newSession(ctx, src, bs)
	defer sess.close()
	sess.state.SetVariable(variable)
	sess.state.flush()
	return nil
}

// SetSourceLoginInfo 直接覆盖保存登录信息（前端表单保存，不触发登录动作）。
func (s *ReaderService) SetSourceLoginInfo(ctx context.Context, sourceID string, fields map[string]string) error {
	src, bs, err := s.loadSource(ctx, sourceID)
	if err != nil {
		return err
	}
	sess := s.newSession(ctx, src, bs)
	defer sess.close()
	b, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	sess.state.SetLoginInfo(string(b))
	sess.state.flush()
	return nil
}

// ClearSourceLogin 清除登录态：登录信息与全部 Cookie（对应 legado logout）。
func (s *ReaderService) ClearSourceLogin(ctx context.Context, sourceID string) error {
	src, bs, err := s.loadSource(ctx, sourceID)
	if err != nil {
		return err
	}
	sess := s.newSession(ctx, src, bs)
	defer sess.close()
	sess.state.SetLoginInfo("")
	sess.state.SetLoginHeader("")
	sess.state.clearCookies()
	sess.state.flush()
	return nil
}

// loginBindings 构造登录动作的 JS 作用域绑定。
// 对应 legado：result 为表单数据；book/chapter/isLongClick 一并提供。
func loginBindings(sess *sourceSession, values map[string]string) map[string]any {
	if values == nil {
		values = map[string]string{}
	}
	return map[string]any{
		"result":      values,
		"book":        nil,
		"chapter":     nil,
		"isLongClick": false,
		"sourceUrl":   sess.srcURL(),
	}
}

func parseLoginInfoValues(raw string) (map[string]string, error) {
	out := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// isJSWrapped 判断规则串是否带 @js: / <js> 包裹。
func isJSWrapped(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "@js:") || strings.HasPrefix(s, "<js>")
}

func anyToStr(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}
