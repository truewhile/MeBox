package rule

import (
	"bytes"
	"strings"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
)

// 本文件在 golang.org/x/net/html 的 *html.Node 上实现 jsoup 的元素语义，
// 供 jsoup 风格分析器使用。语义对齐 org.jsoup.nodes.Element。

func isElement(n *html.Node) bool {
	return n != nil && n.Type == html.ElementNode
}

// childrenElements 对应 Element.children()：直接子元素。
func childrenElements(n *html.Node) []*html.Node {
	var out []*html.Node
	if n == nil {
		return out
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if isElement(c) {
			out = append(out, c)
		}
	}
	return out
}

// collectElements 对应 jsoup Collector.collect(evaluator, root)：
// 前序遍历，包含 root 自身。
func collectElements(root *html.Node, pred func(*html.Node) bool) []*html.Node {
	var out []*html.Node
	if root == nil {
		return out
	}
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && pred(n) {
			out = append(out, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	// root 自身参与匹配（jsoup select/getElementsByXxx 均包含自身）
	if root.Type == html.ElementNode && pred(root) {
		out = append(out, root)
	}
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		walk(c)
	}
	return out
}

func hasClassToken(n *html.Node, class string) bool {
	for _, a := range n.Attr {
		if a.Key == "class" {
			for _, tok := range strings.Fields(a.Val) {
				if tok == class {
					return true
				}
			}
		}
	}
	return false
}

// getElementsByClass 对应 Element.getElementsByClass（含自身）。
func getElementsByClass(root *html.Node, class string) []*html.Node {
	return collectElements(root, func(n *html.Node) bool { return hasClassToken(n, class) })
}

// getElementsByTag 对应 Element.getElementsByTag（含自身）。
func getElementsByTag(root *html.Node, tag string) []*html.Node {
	return collectElements(root, func(n *html.Node) bool { return n.Data == tag })
}

// getElementsById 对应 Collector.collect(Evaluator.Id(id), root)（含自身）。
func getElementsById(root *html.Node, id string) []*html.Node {
	return collectElements(root, func(n *html.Node) bool {
		for _, a := range n.Attr {
			if a.Key == "id" && a.Val == id {
				return true
			}
		}
		return false
	})
}

// getElementsContainingOwnText 对应 Evaluator.ContentsOwnText 语义：
// ownText 包含目标串的元素。
func getElementsContainingOwnText(root *html.Node, text string) []*html.Node {
	return collectElements(root, func(n *html.Node) bool {
		return strings.Contains(nodeOwnText(n), text)
	})
}

// selectCSS 对应 Element.select(css)：以 root 为起点（含自身）执行 CSS 选择。
func selectCSS(root *html.Node, sel string) []*html.Node {
	compiled, err := cascadia.Compile(sel)
	if err != nil {
		return nil
	}
	return selectWithCompiled(root, compiled)
}

func selectWithCompiled(root *html.Node, sel cascadia.Selector) []*html.Node {
	var out []*html.Node
	if root.Type == html.ElementNode && sel(root) {
		out = append(out, root)
	}
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if isElement(c) && sel(c) {
				out = append(out, c)
			}
			walk(c)
		}
	}
	walk(root)
	return out
}

// nodeOwnText 对应 Element.ownText()：直接子文本节点，规整空白后空格连接。
func nodeOwnText(n *html.Node) string {
	var parts []string
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			t := normalizeWhitespace(c.Data)
			if t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, " ")
}

// nodeText 对应 Element.text()：全部后代文本规整空白（<br> 记空格，
// 跳过 script/style），多段空白折叠为单个空格。
func nodeText(n *html.Node) string {
	var sb bytes.Buffer
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
			return
		}
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		if n.Type == html.ElementNode && n.Data == "br" {
			sb.WriteString(" ")
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return normalizeWhitespace(sb.String())
}

// normalizeWhitespace 对应 jsoup TextUtil 的空白规整。
func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// nodeTextNodes 对应 Element.textNodes()：直接子文本节点（trim 非空）。
func nodeTextNodes(n *html.Node) []string {
	var out []string
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			t := strings.TrimSpace(c.Data)
			if t != "" {
				out = append(out, t)
			}
		}
	}
	return out
}

// nodeData 对应 Element.data()：script/style 的原始内容。
func nodeData(n *html.Node) string {
	if n.Type != html.ElementNode || (n.Data != "script" && n.Data != "style") {
		return ""
	}
	var sb bytes.Buffer
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			sb.WriteString(c.Data)
		}
	}
	return sb.String()
}

// outerHTML 对应 Element.outerHtml()。
func outerHTML(n *html.Node) string {
	var buf bytes.Buffer
	if err := html.Render(&buf, n); err != nil {
		return ""
	}
	return buf.String()
}

// outerHTMLNoScript 对应 getResultLast "html" 分支：移除 script/style 后的 outerHtml。
func outerHTMLNoScript(n *html.Node) string {
	clone := cloneNodeShallowTree(n)
	removeTags(clone, "script")
	removeTags(clone, "style")
	return outerHTML(clone)
}

func removeTags(n *html.Node, tag string) {
	var toRemove []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if isElement(c) && c.Data == tag {
			toRemove = append(toRemove, c)
		}
		removeTags(c, tag)
	}
	for _, r := range toRemove {
		n.RemoveChild(r)
	}
}

// cloneNodeShallowTree 深拷贝节点树（html.Render 需要）。
func cloneNodeShallowTree(n *html.Node) *html.Node {
	c := &html.Node{
		Type:     n.Type,
		DataAtom: n.DataAtom,
		Data:     n.Data,
		Attr:     append([]html.Attribute(nil), n.Attr...),
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		cc := cloneNodeShallowTree(ch)
		c.AppendChild(cc)
	}
	return c
}

func attrValue(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// parseHTML 对应 AnalyzeByJSoup.parse：字符串转节点树。
// x/net/html 会补全 <html><body> 结构，选择器从 document 根开始匹配，
// 与 jsoup 以 Document 为根的选择行为一致。
func parseHTML(s string) *html.Node {
	nodes, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return nil
	}
	return nodes
}
