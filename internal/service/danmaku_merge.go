package service

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// 弹幕合并：同一集在聚合源（如 LogVar）里常有多个库，开启合并后把这些库的
// 弹幕一起去重后展示，而不是只显示其中一个。
const (
	// danmakuMergeMaxSources 限制一次合并涉及的来源数量，避免把一次播放
	// 变成几十个上游请求。
	danmakuMergeMaxSources = 10
	// danmakuMergeConcurrency 限制并发抓取数，降低触发上游限流（429）的概率。
	danmakuMergeConcurrency = 4
	// danmakuMergeTimeToleranceSec 是判定「同一时间点」的容差。同一条弹幕在
	// 不同源之间可能因精度处理差上零点几秒，用容差比对；文本仍要求完全一致，
	// 因此不会把内容不同的弹幕误合。
	danmakuMergeTimeToleranceSec = 0.5
)

// danmakuComment 是合并用的归一化弹幕。
type danmakuComment struct {
	TimeSec float64
	Mode    int
	Color   int
	Text    string
}

// parseDanmakuComments 把上游载荷解析成归一化弹幕列表，兼容 dandanplay
// JSON（{comments:[{p,m}]}）与 Bilibili XML（<d p="...">text</d>）两种格式。
// 无法识别的载荷返回空列表，调用方据此跳过该来源。
func parseDanmakuComments(raw string) []danmakuComment {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		if comments := parseDanmakuCommentsJSON(trimmed); len(comments) > 0 {
			return comments
		}
	}
	if strings.HasPrefix(trimmed, "<") {
		return parseDanmakuCommentsXML(trimmed)
	}
	return nil
}

func parseDanmakuCommentsJSON(raw string) []danmakuComment {
	// 兼容 {comments:[...]} 与裸数组两种形态。
	var payload struct {
		Comments []struct {
			P string `json:"p"`
			M string `json:"m"`
			// 少数自建源直接给结构化字段。
			Time  *float64 `json:"time"`
			Text  string   `json:"text"`
			Mode  *int     `json:"mode"`
			Color *int     `json:"color"`
		} `json:"comments"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		var bare []struct {
			P     string   `json:"p"`
			M     string   `json:"m"`
			Time  *float64 `json:"time"`
			Text  string   `json:"text"`
			Mode  *int     `json:"mode"`
			Color *int     `json:"color"`
		}
		if err2 := json.Unmarshal([]byte(raw), &bare); err2 != nil {
			return nil
		}
		payload.Comments = bare
	}
	out := make([]danmakuComment, 0, len(payload.Comments))
	for _, item := range payload.Comments {
		comment, ok := buildDanmakuComment(item.P, item.M, item.Time, item.Text, item.Mode, item.Color)
		if ok {
			out = append(out, comment)
		}
	}
	return out
}

func parseDanmakuCommentsXML(raw string) []danmakuComment {
	var doc struct {
		Items []struct {
			P    string `xml:"p,attr"`
			Text string `xml:",chardata"`
		} `xml:"d"`
	}
	if err := xml.Unmarshal([]byte(raw), &doc); err != nil {
		return nil
	}
	out := make([]danmakuComment, 0, len(doc.Items))
	for _, item := range doc.Items {
		comment, ok := buildDanmakuComment(item.P, item.Text, nil, "", nil, nil)
		if ok {
			out = append(out, comment)
		}
	}
	return out
}

// buildDanmakuComment 从 p 串或结构化字段构造一条弹幕。p 串格式为
// "time,mode,color,user"（dandanplay 四段式）。
func buildDanmakuComment(p, text string, timeSec *float64, plainText string, mode, color *int) (danmakuComment, bool) {
	body := strings.TrimSpace(text)
	if body == "" {
		body = strings.TrimSpace(plainText)
	}
	if body == "" {
		return danmakuComment{}, false
	}
	comment := danmakuComment{Text: body, Mode: 1}
	if timeSec != nil {
		comment.TimeSec = *timeSec
	}
	if mode != nil && *mode > 0 {
		comment.Mode = *mode
	}
	if color != nil {
		comment.Color = *color
	}
	if fields := strings.Split(p, ","); len(fields) >= 1 {
		if t, err := strconv.ParseFloat(strings.TrimSpace(fields[0]), 64); err == nil {
			comment.TimeSec = t
		}
		if len(fields) >= 2 {
			if m, err := strconv.Atoi(strings.TrimSpace(fields[1])); err == nil && m > 0 {
				comment.Mode = m
			}
		}
		// 颜色所在位置取决于格式，用段数区分（与前端 parseBilibiliXml 的判定
		// 一致）：Bilibili 的 p 是 "time,mode,fontSize,color,..."（>=5 段，
		// 颜色在第 4 段）；dandanplay 的 p 是 "time,mode,color,userId"（4 段，
		// 颜色在第 3 段）。不区分会把字号当成颜色。
		colorIndex := 2
		if len(fields) >= 5 {
			colorIndex = 3
		}
		if len(fields) > colorIndex {
			if c, err := strconv.Atoi(strings.TrimSpace(fields[colorIndex])); err == nil {
				comment.Color = c
			}
		}
	}
	if math.IsNaN(comment.TimeSec) || math.IsInf(comment.TimeSec, 0) || comment.TimeSec < 0 {
		return danmakuComment{}, false
	}
	// 无颜色信息时用白色，与前端默认一致。
	if comment.Color <= 0 {
		comment.Color = 16777215
	}
	return comment, true
}

// mergeDanmakuComments 合并多组弹幕并按「时间 + 内容」去重。
//
// 判定重复的条件：文本完全一致，且时间差在 danmakuMergeTimeToleranceSec 以内。
// 之所以同时要求文本一致，是因为容差本身不足以区分内容；之所以需要容差，
// 是因为同一条弹幕在不同来源间可能因精度处理差上零点几秒。
func mergeDanmakuComments(sets [][]danmakuComment) []danmakuComment {
	merged := make([]danmakuComment, 0, 512)
	// keptTimes[文本] = 已保留的该文本时间列表，用于就近比对。
	keptTimes := make(map[string][]float64)
	for _, set := range sets {
		for _, comment := range set {
			times := keptTimes[comment.Text]
			duplicate := false
			for _, kept := range times {
				if math.Abs(kept-comment.TimeSec) <= danmakuMergeTimeToleranceSec {
					duplicate = true
					break
				}
			}
			if duplicate {
				continue
			}
			keptTimes[comment.Text] = append(times, comment.TimeSec)
			merged = append(merged, comment)
		}
	}
	sort.SliceStable(merged, func(i, j int) bool { return merged[i].TimeSec < merged[j].TimeSec })
	return merged
}

// encodeDanmakuComments 把合并结果编码成 dandanplay JSON，前端 parseDanmaku
// 已支持该格式（{comments:[{p,m}]}）。
func encodeDanmakuComments(comments []danmakuComment) string {
	type item struct {
		Cid int    `json:"cid"`
		P   string `json:"p"`
		M   string `json:"m"`
		T   int    `json:"t"`
	}
	payload := struct {
		Count    int    `json:"count"`
		Comments []item `json:"comments"`
	}{Count: len(comments), Comments: make([]item, 0, len(comments))}
	for i, comment := range comments {
		payload.Comments = append(payload.Comments, item{
			Cid: i + 1,
			P:   fmt.Sprintf("%.2f,%d,%d,merged", comment.TimeSec, comment.Mode, comment.Color),
			M:   comment.Text,
			T:   int(comment.TimeSec),
		})
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(encoded)
}
