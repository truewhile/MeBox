// Package rule 移植自 legado（refgd/legado，GPL-3.0）的规则引擎：
// io.legado.app.model.analyzeRule 包下的 AnalyzeRule / AnalyzeByJSoup /
// AnalyzeByJSonPath / AnalyzeByXPath / AnalyzeByRegex / AnalyzeUrl / RuleAnalyzer。
// 语义以 legado 源码为基准逐项对齐，注释中标注了对应的 Kotlin 方法名。
package rule

import "errors"

var (
	// ErrJsUnsupported 书源规则中包含 JS（<js>/@js:/{{}}/js 选项）。
	// JS 引擎（goja + java.* 桥）在 P2 阶段接入，届时移除本错误路径。
	ErrJsUnsupported = errors.New("书源使用了 JS 规则，当前阶段暂不支持")
	// ErrWebJSUnsupported 书源依赖 webView/webJs 抓取，服务端无头浏览器不在支持范围。
	ErrWebJSUnsupported = errors.New("书源依赖 webView 抓取，暂不支持")
	// ErrTypeUnsupported 书源 URL 声明了 type（zip/file 等），暂不支持。
	ErrTypeUnsupported = errors.New("书源 URL 声明了不支持的 type")
)
