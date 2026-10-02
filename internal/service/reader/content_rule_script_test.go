package reader

import (
	"strings"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

// TestContentRuleJSReceivesJoinedString 覆盖拷贝漫画（优++）的正文规则写法：
//
//	$..url
//	<js>result.split("\n").map(x=>'<img src="'+x+'">').join("\n")</js>
//
// JSON 段先命中「多张图片的 url 列表」，JS 段拿到的必须是按 \n 拼好的字符串
// （对应 legado BookContent 用 analyzeRule.getString 求值正文规则），
// 喂成数组就会 TypeError: Object has no member 'split'，整章图片全取不到。
func TestContentRuleJSReceivesJoinedString(t *testing.T) {
	svc, _ := newLoginTestService(t)

	const srcURL = "https://www.mangacopy.com/"
	const contentRule = "$..url\n<js>result.split(\"\\n\").map(x=>'<img src=\"'+x+'\">').join(\"\\n\")</js>"
	header := ""
	src := &model.ReaderBookSource{Name: "拷贝漫画（优++）", SourceURL: srcURL}
	bs := &BookSource{
		BookSourceURL: srcURL,
		RuleContent:   &ContentRule{Content: strPtr(contentRule)},
		Header:        &header,
	}

	body := `{"results":{"chapter":{"contents":[` +
		`{"url":"https://img.example.com/1.webp"},` +
		`{"url":"https://img.example.com/2.webp"}]}}}`

	sess := svc.newSession(t.Context(), src, bs)
	defer sess.close()
	ar := sess.newAnalyzer("", 0, body, "https://api.mangacopy.com/api/v3/chapter/x")

	got, err := ar.GetString(contentRule, nil, false)
	if err != nil {
		t.Fatalf("正文规则求值失败: %v", err)
	}
	want := "<img src=\"https://img.example.com/1.webp\">\n<img src=\"https://img.example.com/2.webp\">"
	if got != want {
		t.Fatalf("正文 = %q\n期望 %q", got, want)
	}
	// 图片提取（漫画分支）：每行一个 <img>，应抽出 2 个真实地址
	refs := 0
	for _, line := range strings.Split(got, "\n") {
		refs += len(imageRefsInLine(line))
	}
	if refs != 2 {
		t.Fatalf("从正文抽出 %d 张图，期望 2", refs)
	}
}
