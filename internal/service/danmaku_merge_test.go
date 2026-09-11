package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseDanmakuCommentsSupportsDandanplayJson(t *testing.T) {
	raw := `{"count":2,"comments":[` +
		`{"cid":1,"p":"1.50,1,16777215,userA","m":"第一条"},` +
		`{"cid":2,"p":"2.00,5,16711680,userB","m":"顶部红字"}]}`
	comments := parseDanmakuComments(raw)
	require.Len(t, comments, 2)
	require.Equal(t, 1.5, comments[0].TimeSec)
	require.Equal(t, 1, comments[0].Mode)
	require.Equal(t, 16777215, comments[0].Color)
	require.Equal(t, "第一条", comments[0].Text)
	// 第 5 种模式是顶部弹幕，颜色 16711680 = 0xFF0000。
	require.Equal(t, 5, comments[1].Mode)
	require.Equal(t, 16711680, comments[1].Color)
}

func TestParseDanmakuCommentsSupportsBilibiliXml(t *testing.T) {
	raw := `<?xml version="1.0" encoding="UTF-8"?><i>` +
		`<d p="3.25,1,25,16777215,1700000000,0,abc,def">来自 XML</d>` +
		`</i>`
	comments := parseDanmakuComments(raw)
	require.Len(t, comments, 1)
	require.Equal(t, 3.25, comments[0].TimeSec)
	require.Equal(t, "来自 XML", comments[0].Text)
	// Bilibili 的 5 段式里第 3 段是字号、第 4 段才是颜色，必须取第 4 段。
	require.Equal(t, 16777215, comments[0].Color)
}

func TestParseDanmakuCommentsReturnsNilForUnsupportedPayload(t *testing.T) {
	require.Nil(t, parseDanmakuComments(""))
	require.Nil(t, parseDanmakuComments("not a danmaku payload"))
}

// 同一时间 + 同一内容视为重复，只保留一条。
func TestMergeDanmakuCommentsDeduplicatesByTimeAndText(t *testing.T) {
	setA := []danmakuComment{
		{TimeSec: 1.0, Mode: 1, Color: 16777215, Text: "哈哈"},
		{TimeSec: 5.0, Mode: 1, Color: 16777215, Text: "只有A有"},
	}
	setB := []danmakuComment{
		{TimeSec: 1.0, Mode: 1, Color: 16777215, Text: "哈哈"}, // 与 A 完全重复
		{TimeSec: 5.2, Mode: 1, Color: 16777215, Text: "只有B有"},
	}
	merged := mergeDanmakuComments([][]danmakuComment{setA, setB})
	require.Len(t, merged, 3)
	require.Equal(t, 1.0, merged[0].TimeSec)
	require.Equal(t, "哈哈", merged[0].Text)
	require.Equal(t, "只有A有", merged[1].Text)
	require.Equal(t, "只有B有", merged[2].Text)
}

// 时间差在容差内且文本一致时也判为重复（跨源可能有零点几秒的精度差）。
func TestMergeDanmakuCommentsDeduplicatesWithinTolerance(t *testing.T) {
	merged := mergeDanmakuComments([][]danmakuComment{
		{{TimeSec: 10.0, Mode: 1, Text: "2333"}},
		{{TimeSec: 10.3, Mode: 1, Text: "2333"}},
	})
	require.Len(t, merged, 1)
	require.Equal(t, 10.0, merged[0].TimeSec)
}

// 内容相同但时间相距较远时是两条独立弹幕，不能合并。
func TestMergeDanmakuCommentsKeepsSameTextAtDifferentTimes(t *testing.T) {
	merged := mergeDanmakuComments([][]danmakuComment{
		{{TimeSec: 1.0, Mode: 1, Text: "前方高能"}},
		{{TimeSec: 30.0, Mode: 1, Text: "前方高能"}},
	})
	require.Len(t, merged, 2)
}

// 时间相同但内容不同也不能合并。
func TestMergeDanmakuCommentsKeepsDifferentTextAtSameTime(t *testing.T) {
	merged := mergeDanmakuComments([][]danmakuComment{
		{{TimeSec: 2.0, Mode: 1, Text: "AAA"}},
		{{TimeSec: 2.0, Mode: 1, Text: "BBB"}},
	})
	require.Len(t, merged, 2)
}

func TestMergeDanmakuCommentsSortsByTime(t *testing.T) {
	merged := mergeDanmakuComments([][]danmakuComment{
		{{TimeSec: 9.0, Mode: 1, Text: "后"}},
		{{TimeSec: 1.0, Mode: 1, Text: "前"}},
	})
	require.Len(t, merged, 2)
	require.Equal(t, 1.0, merged[0].TimeSec)
	require.Equal(t, 9.0, merged[1].TimeSec)
}

// 编码结果必须能被前端的 dandanplay JSON 分支解析（p + m 两个字符串字段）。
func TestEncodeDanmakuCommentsProducesDandanplayShape(t *testing.T) {
	encoded := encodeDanmakuComments([]danmakuComment{
		{TimeSec: 1.5, Mode: 5, Color: 16711680, Text: "顶部"},
	})
	var payload struct {
		Count    int `json:"count"`
		Comments []struct {
			Cid int    `json:"cid"`
			P   string `json:"p"`
			M   string `json:"m"`
			T   int    `json:"t"`
		} `json:"comments"`
	}
	require.NoError(t, json.Unmarshal([]byte(encoded), &payload))
	require.Equal(t, 1, payload.Count)
	require.Len(t, payload.Comments, 1)
	require.Equal(t, "1.50,5,16711680,merged", payload.Comments[0].P)
	require.Equal(t, "顶部", payload.Comments[0].M)

	// 回环：编码后的载荷应能再次解析出一致的弹幕。
	roundTrip := parseDanmakuComments(encoded)
	require.Len(t, roundTrip, 1)
	require.Equal(t, 1.5, roundTrip[0].TimeSec)
	require.Equal(t, 5, roundTrip[0].Mode)
	require.Equal(t, 16711680, roundTrip[0].Color)
	require.Equal(t, "顶部", roundTrip[0].Text)
}
