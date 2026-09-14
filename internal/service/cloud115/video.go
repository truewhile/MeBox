// 115 开放平台视频播放/转码 API。
//
// 播放接口返回的是 115 云端转码后的 HLS（m3u8）地址，不是原文件
// downurl；未完成转码时接口会返回 state=false，可再调用 video_push
// 请求加速转码。
package cloud115

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// FlexString 兼容 115 接口同字段在不同设备端返回字符串或数字的情况。
type FlexString string

func (s *FlexString) UnmarshalJSON(data []byte) error {
	raw := bytes.TrimSpace(data)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		*s = ""
		return nil
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		*s = FlexString(str)
		return nil
	}
	var num json.Number
	if err := json.Unmarshal(raw, &num); err == nil {
		*s = FlexString(num.String())
		return nil
	}
	return fmt.Errorf("115: 无法解析字符串/数字字段 %s", string(raw))
}

func (s FlexString) String() string {
	return string(s)
}

func (s FlexString) Int64() int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(string(s)), 10, 64)
	return v
}

// MultiTrack 是 115 返回的一条音轨信息。
type MultiTrack struct {
	Title      string `json:"title"`
	IsSelected string `json:"is_selected"`
	SyncTime   string `json:"sync_time"`
}

// MultiTrackList 兼容 115 接口把 multitrack_list 返回成对象（键为音轨序号）
// 或数组的两种形态。
type MultiTrackList []MultiTrack

func (l *MultiTrackList) UnmarshalJSON(data []byte) error {
	raw := bytes.TrimSpace(data)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) || bytes.Equal(raw, []byte("{}")) {
		*l = nil
		return nil
	}
	if raw[0] == '[' {
		var arr []MultiTrack
		if err := json.Unmarshal(raw, &arr); err != nil {
			return err
		}
		*l = arr
		return nil
	}
	if raw[0] == '{' {
		var m map[string]MultiTrack
		if err := json.Unmarshal(raw, &m); err != nil {
			return err
		}
		keys := make([]int, 0, len(m))
		for key := range m {
			idx, err := strconv.Atoi(strings.TrimSpace(key))
			if err != nil {
				continue
			}
			keys = append(keys, idx)
		}
		sort.Ints(keys)
		out := make([]MultiTrack, 0, len(keys))
		for _, idx := range keys {
			out = append(out, m[strconv.Itoa(idx)])
		}
		*l = out
		return nil
	}
	return fmt.Errorf("115: multitrack_list 既不是对象也不是数组")
}

// VideoURLItem 是 115 返回的一条清晰度播放地址。
type VideoURLItem struct {
	URL         string `json:"url"`
	Height      int    `json:"height"`
	Width       int    `json:"width"`
	Definition  int    `json:"definition"`
	Title       string `json:"title"`
	DefinitionN int    `json:"definition_n"`
}

// VideoPlayData 是 /open/video/play 的 data 字段。
type VideoPlayData struct {
	FileID            string            `json:"file_id"`
	ParentID          string            `json:"parent_id"`
	FileName          string            `json:"file_name"`
	FileSize          FlexString        `json:"file_size"`
	FileSha1          string            `json:"file_sha1"`
	FileType          string            `json:"file_type"`
	IsPrivate         FlexString        `json:"is_private"`
	PlayLong          FlexString        `json:"play_long"`
	UserDef           int               `json:"user_def"`
	UserRotate        int               `json:"user_rotate"`
	UserTurn          int               `json:"user_turn"`
	MultitrackList    MultiTrackList    `json:"multitrack_list"`
	DefinitionList    map[string]string `json:"definition_list"`
	DefinitionListNew map[string]string `json:"definition_list_new"`
	VideoURL          []VideoURLItem    `json:"video_url"`
	VideoPushState    *bool             `json:"video_push_state"`
}

// GetVideoPlayInfo 调用 115 视频在线播放接口。
//
// 注意：state=false（例如视频尚未转码）时 doAuthJSON 会返回 error，但仍会
// 返回已解析的 RespBase/VideoPlayData。调用方需要同时检查两者：有 data 时
// 可用于判断需要等待还是触发加速转码。
func (c *OpenClient) GetVideoPlayInfo(ctx context.Context, pickCode, ua string) (*RespBase, *VideoPlayData, error) {
	pickCode = strings.TrimSpace(pickCode)
	if pickCode == "" {
		return nil, nil, fmt.Errorf("115: pick_code 为空")
	}
	resp, err := c.doAuthJSONWithUA(
		ctx,
		http.MethodGet,
		ProAPIBase+"/open/video/play",
		map[string]string{"pick_code": pickCode},
		1,
		ua,
	)
	if resp == nil {
		return nil, nil, err
	}
	data := &VideoPlayData{}
	if len(resp.Data) > 0 {
		trimmed := bytes.TrimSpace(resp.Data)
		if !bytes.Equal(trimmed, []byte("null")) {
			if decodeErr := json.Unmarshal(trimmed, data); decodeErr != nil && err == nil {
				err = fmt.Errorf("115: 解析视频播放信息失败：%w", decodeErr)
			}
		}
	}
	return resp, data, err
}

// SubmitVideoPush 提交 115 加速转码请求。
//
// op 支持 vip_push（按 VIP 等级加速）和 pay_push（消耗枫币）。调用方必须
// 明确选择 op，默认空值按 vip_push 处理，避免误消费枫币。
func (c *OpenClient) SubmitVideoPush(ctx context.Context, pickCode, op string) error {
	pickCode = strings.TrimSpace(pickCode)
	if pickCode == "" {
		return fmt.Errorf("115: pick_code 为空")
	}
	op = strings.TrimSpace(op)
	if op == "" {
		op = "vip_push"
	}
	_, err := c.doAuthJSON(
		ctx,
		http.MethodPost,
		ProAPIBase+"/open/video/video_push",
		map[string]string{
			"pick_code": pickCode,
			"op":        op,
		},
		1,
	)
	return err
}
