// reader-smoke 是阅读书源兼容性冒烟工具：
// 对批量书源逐个跑「搜索 → 详情 → 目录 → 正文」全链路，输出兼容率报告。
//
// 用法：
//
//	go run ./cmd/reader-smoke -file sources.json -key 斗破苍穹 -c 8
//	go run ./cmd/reader-smoke -url https://example.com/sources.json -json > report.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/helper"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service/reader"
)

func main() {
	file := flag.String("file", "", "书源文件路径（JSON 数组/对象/Base64/每行一个）")
	urlFlag := flag.String("url", "", "书源网络地址（与 -file 二选一）")
	key := flag.String("key", "斗破苍穹", "搜索关键词")
	concurrency := flag.Int("c", 4, "并发数")
	timeout := flag.Int("timeout", 90, "单源全链路超时（秒）")
	jsonOut := flag.Bool("json", false, "输出完整 JSON 报告（追加在汇总后）")
	flag.Parse()

	payload := ""
	switch {
	case *file != "":
		data, err := os.ReadFile(*file)
		if err != nil {
			fatal("读取文件失败: %v", err)
		}
		payload = string(data)
	case *urlFlag != "":
		client := helper.NewSiteHTTPClient(30, true)
		req, err := http.NewRequest("GET", *urlFlag, nil)
		if err != nil {
			fatal("构造请求失败: %v", err)
		}
		for k, v := range helper.HTTPHeaderPresets() {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			fatal("拉取书源失败: %v", err)
		}
		defer resp.Body.Close()
		var sb strings.Builder
		buf := make([]byte, 32*1024)
		for {
			n, err := resp.Body.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				break
			}
		}
		payload = sb.String()
	default:
		fatal("需要 -file 或 -url 指定书源来源")
	}

	sources := reader.ParseSourcePayload(payload)
	if len(sources) == 0 {
		fatal("未从输入中识别到书源")
	}

	svc := reader.NewReaderService(&config.Config{}, zap.NewNop(), &repository.Container{})
	ctx := context.Background()

	results := make([]*reader.SmokeChainResult, len(sources))
	sem := make(chan struct{}, max(1, *concurrency))
	var wg sync.WaitGroup
	for i, raw := range sources {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, raw string) {
			defer wg.Done()
			defer func() { <-sem }()
			ctxSrc, cancel := context.WithTimeout(ctx, time.Duration(*timeout)*time.Second)
			defer cancel()
			res := svc.SmokeSource(ctxSrc, raw, *key)
			results[i] = res
			status := "✓"
			if !res.OK {
				status = "✗"
			}
			fmt.Fprintf(os.Stderr, "%s %-24s [%s] hits=%d chapters=%d content=%d %s\n",
				status, truncate(res.SourceName, 24), stageCN(res), res.SearchHits, res.Chapters, res.ContentLen, res.Error)
		}(i, raw)
	}
	wg.Wait()

	// 汇总
	var searchOK, infoOK, tocOK, contentOK, allOK int
	failedAt := map[string]int{}
	for _, r := range results {
		if r == nil {
			continue
		}
		switch r.FailedAt {
		case "":
			allOK++
			searchOK++
			infoOK++
			tocOK++
			contentOK++
		case "search":
			failedAt["search"]++
		case "info":
			searchOK++
			failedAt["info"]++
		case "toc":
			searchOK++
			infoOK++
			failedAt["toc"]++
		case "content":
			searchOK++
			infoOK++
			tocOK++
			failedAt["content"]++
		}
	}
	n := len(results)
	pct := func(v int) string {
		if n == 0 {
			return "0%"
		}
		return fmt.Sprintf("%.1f%%", float64(v)/float64(n)*100)
	}
	fmt.Printf("\n==== 冒烟报告 ====\n")
	fmt.Printf("书源总数: %d  关键词: %s\n", n, *key)
	fmt.Printf("搜索通过: %d (%s)\n", searchOK, pct(searchOK))
	fmt.Printf("详情通过: %d (%s)\n", infoOK, pct(infoOK))
	fmt.Printf("目录通过: %d (%s)\n", tocOK, pct(tocOK))
	fmt.Printf("正文通过: %d (%s)\n", contentOK, pct(contentOK))
	fmt.Printf("全链路通过: %d (%s)\n", allOK, pct(allOK))
	for _, stage := range []string{"search", "info", "toc", "content"} {
		if failedAt[stage] > 0 {
			fmt.Printf("  失败于 %s: %d\n", stageCN(&reader.SmokeChainResult{FailedAt: stage}), failedAt[stage])
		}
	}

	if *jsonOut {
		out, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			fatal("序列化报告失败: %v", err)
		}
		fmt.Println(string(out))
	}
}

func stageCN(r *reader.SmokeChainResult) string {
	switch r.FailedAt {
	case "":
		return "完成"
	case "search":
		return "搜索"
	case "info":
		return "详情"
	case "toc":
		return "目录"
	case "content":
		return "正文"
	case "parse":
		return "解析"
	default:
		return r.FailedAt
	}
}

func truncate(s string, n int) string {
	rs := []rune(strings.TrimSpace(s))
	if len(rs) <= n {
		if len(rs) == 0 {
			return "(未命名)"
		}
		return string(rs)
	}
	return string(rs[:n]) + "…"
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "reader-smoke: "+format+"\n", args...)
	os.Exit(1)
}
