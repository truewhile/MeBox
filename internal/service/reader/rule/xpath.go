package rule

import (
	"strings"

	"github.com/antchfx/htmlquery"
	"golang.org/x/net/html"
)

// 本文件对应 AnalyzeByXPath.kt（JsoupXpath 语义，Go 侧用 antchfx/htmlquery）。

type xpathAnalyzer struct {
	root *html.Node
}

func newXPathAnalyzer(doc any) *xpathAnalyzer {
	return &xpathAnalyzer{root: toHTMLNode(doc)}
}

// xpathResult 对应 getResult(xPath)。
func (a *xpathAnalyzer) result(xPath string) []*html.Node {
	if a.root == nil || xPath == "" {
		return nil
	}
	return htmlquery.Find(a.root, xPath)
}

// xpathNodeString 对应 JXNode.asString()。
func xpathNodeString(n *html.Node) string {
	if n == nil {
		return ""
	}
	if n.Type == html.TextNode {
		return n.Data
	}
	if n.Type == html.ElementNode {
		return htmlquery.InnerText(n)
	}
	if n.Data != "" {
		return n.Data
	}
	return ""
}

// getElements 对应 AnalyzeByXPath.getElements。
func (a *xpathAnalyzer) getElements(xPath string) []*html.Node {
	if xPath == "" {
		return nil
	}
	ra := NewRuleAnalyzer(xPath, false)
	rules := ra.SplitRule("&&", "||", "%%")
	if len(rules) == 1 {
		return a.result(rules[0])
	}
	var out []*html.Node
	var results [][]*html.Node
	for _, rl := range rules {
		temp := a.getElements(rl)
		if len(temp) > 0 {
			results = append(results, temp)
			if ra.ElementsType() == "||" {
				break
			}
		}
	}
	if len(results) > 0 {
		if ra.ElementsType() == "%%" {
			for i := 0; i < len(results[0]); i++ {
				for _, temp := range results {
					if i < len(temp) {
						out = append(out, temp[i])
					}
				}
			}
		} else {
			for _, temp := range results {
				out = append(out, temp...)
			}
		}
	}
	return out
}

// getStringList 对应 AnalyzeByXPath.getStringList。
func (a *xpathAnalyzer) getStringList(xPath string) []string {
	var result []string
	ra := NewRuleAnalyzer(xPath, false)
	rules := ra.SplitRule("&&", "||", "%%")
	if len(rules) == 1 {
		for _, n := range a.result(xPath) {
			result = append(result, xpathNodeString(n))
		}
		return result
	}
	var results [][]string
	for _, rl := range rules {
		temp := a.getStringList(rl)
		if len(temp) > 0 {
			results = append(results, temp)
			if ra.ElementsType() == "||" {
				break
			}
		}
	}
	if len(results) > 0 {
		if ra.ElementsType() == "%%" {
			for i := 0; i < len(results[0]); i++ {
				for _, temp := range results {
					if i < len(temp) {
						result = append(result, temp[i])
					}
				}
			}
		} else {
			for _, temp := range results {
				result = append(result, temp...)
			}
		}
	}
	return result
}

// getString 对应 AnalyzeByXPath.getString：多节点以 \n 连接。
func (a *xpathAnalyzer) getString(rule string) string {
	ra := NewRuleAnalyzer(rule, false)
	rules := ra.SplitRule("&&", "||")
	if len(rules) == 1 {
		nodes := a.result(rule)
		if len(nodes) == 0 {
			return ""
		}
		parts := make([]string, len(nodes))
		for i, n := range nodes {
			parts[i] = xpathNodeString(n)
		}
		return strings.Join(parts, "\n")
	}
	var textList []string
	for _, rl := range rules {
		temp := a.getString(rl)
		if temp != "" {
			textList = append(textList, temp)
			if ra.ElementsType() == "||" {
				break
			}
		}
	}
	return strings.Join(textList, "\n")
}
