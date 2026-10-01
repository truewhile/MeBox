// 115 网盘元数据上传能力：115 开放平台调度 + 阿里云 OSS 直传。
// 参考 QMediaSync 的上传流程实现：
//
//	POST /open/upload/init           上传初始化/秒传调度（含二次签名）
//	GET  /open/upload/get_token      获取 OSS 临时上传凭证（STS）
//	OSS  multipart 分片直传 + callback 完成
//
// 上传目标父目录为 115 目录 ID（cid），而非路径字符串。
package cloud115

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// 115 上传状态码。
const (
	UploadInitStatusNeedUpload    = 1 // 需要真实上传
	UploadInitStatusRapidUploaded = 2 // 秒传成功
	UploadInitStatusSignFailed    = 6 // 签名验证失败
	UploadInitStatusNeedSign      = 7 // 需要二次签名
	UploadInitStatusSignRejected  = 8 // 签名认证失败
)

// UploadInitRequest 是 /open/upload/init 的结构化请求。
type UploadInitRequest struct {
	FileName     string
	FileSize     int64
	ParentFileId string
	FileSha1     string
	Preid        string
	PickCode     string
	TopUpload    string
	SignKey      string
	SignVal      string
}

// UploadInitResult 是 /open/upload/init 的调度结果。
type UploadInitResult struct {
	PickCode  string
	Status    int
	FileId    string
	Target    string
	Bucket    string
	Object    string
	SignKey   string
	SignCheck string
	Callback  UploadResultCallBack
}

type uploadScheduleAPIResult struct {
	PickCode  string          `json:"pick_code"`
	Status    int             `json:"status"`
	FileId    string          `json:"file_id"`
	Target    string          `json:"target"`
	Version   string          `json:"version"`
	Bucket    string          `json:"bucket"`
	Object    string          `json:"object"`
	SignKey   string          `json:"sign_key"`
	SignCheck string          `json:"sign_check"`
	Callback  json.RawMessage `json:"callback"`
}

// UploadResultCallBack 是 init 返回给 OSS complete 使用的 callback 内容。
type UploadResultCallBack struct {
	Callback    string `json:"callback"`
	CallbackVar string `json:"callback_var"`
}

// UploadToken 是 /open/upload/get_token 返回的 OSS STS 临时凭证。
type UploadToken struct {
	Endpoint         string `json:"endpoint"`
	AccessKeySecret  string `json:"AccessKeySecret"`
	AccessKeySecrett string `json:"AccessKeySecrett"`
	SecurityToken    string `json:"SecurityToken"`
	Expiration       string `json:"Expiration"`
	AccessKeyId      string `json:"AccessKeyId"`
}

func (token *UploadToken) normalize() {
	if token == nil {
		return
	}
	if token.AccessKeySecret == "" {
		token.AccessKeySecret = token.AccessKeySecrett
	}
}

// UploadCompleteResult 是 OSS complete callback 成功后的远端文件定位结果。
type UploadCompleteResult struct {
	FileId   string
	PickCode string
	ParentId string
	Sha1     string
	Size     int64
	Mtime    int64
}

// SignCheckRange 是 115 二次认证要求的闭区间 [start,end]。
type SignCheckRange struct {
	Start int64
	End   int64
}

// UploadInit 调用 115 上传初始化/秒传调度接口。
func (c *OpenClient) UploadInit(ctx context.Context, input UploadInitRequest) (*UploadInitResult, error) {
	params := buildUploadInitForm(input)
	resp, err := c.doAuthJSON(ctx, "POST", ProAPIBase+"/open/upload/init", params, 2)
	if err != nil {
		return nil, err
	}
	var raw uploadScheduleAPIResult
	if err := json.Unmarshal(resp.Data, &raw); err != nil {
		return nil, fmt.Errorf("115: 解析 upload/init 结果失败：%w", err)
	}
	callback, err := decodeUploadCallback(raw.Callback)
	if err != nil {
		return nil, err
	}
	return &UploadInitResult{
		PickCode:  raw.PickCode,
		Status:    raw.Status,
		FileId:    raw.FileId,
		Target:    raw.Target,
		Bucket:    raw.Bucket,
		Object:    raw.Object,
		SignKey:   raw.SignKey,
		SignCheck: raw.SignCheck,
		Callback:  callback,
	}, nil
}

func buildUploadInitForm(input UploadInitRequest) map[string]string {
	topUpload := input.TopUpload
	if topUpload == "" {
		topUpload = "0"
	}
	params := map[string]string{
		"file_name": input.FileName,
		"file_size": strconv.FormatInt(input.FileSize, 10),
		"target":    fmt.Sprintf("U_1_%s", input.ParentFileId),
		"fileid":    input.FileSha1,
		"preid":     input.Preid,
		"topupload": topUpload,
	}
	if input.PickCode != "" {
		params["pick_code"] = input.PickCode
	}
	if input.SignKey != "" && input.SignVal != "" {
		params["sign_key"] = input.SignKey
		params["sign_val"] = input.SignVal
	}
	return params
}

func decodeUploadCallback(raw json.RawMessage) (UploadResultCallBack, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return UploadResultCallBack{}, nil
	}
	if raw[0] == '[' {
		var callbacks []UploadResultCallBack
		if err := json.Unmarshal(raw, &callbacks); err != nil {
			return UploadResultCallBack{}, err
		}
		if len(callbacks) == 0 {
			return UploadResultCallBack{}, nil
		}
		return callbacks[0], nil
	}
	var callback UploadResultCallBack
	if err := json.Unmarshal(raw, &callback); err != nil {
		return UploadResultCallBack{}, err
	}
	return callback, nil
}

func parseSignCheckRange(value string) (SignCheckRange, error) {
	parts := strings.Split(value, "-")
	if len(parts) != 2 {
		return SignCheckRange{}, fmt.Errorf("sign_check 格式错误：%s", value)
	}
	start, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil {
		return SignCheckRange{}, err
	}
	end, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil {
		return SignCheckRange{}, err
	}
	if start < 0 || end < start {
		return SignCheckRange{}, fmt.Errorf("sign_check 范围非法：%s", value)
	}
	return SignCheckRange{Start: start, End: end}, nil
}

// GetUploadToken 获取 115 下发的 OSS 临时上传凭证。
func (c *OpenClient) GetUploadToken(ctx context.Context) (*UploadToken, error) {
	resp, err := c.doAuthJSON(ctx, "GET", ProAPIBase+"/open/upload/get_token", nil, 2)
	if err != nil {
		return nil, err
	}
	var token UploadToken
	if err := json.Unmarshal(resp.Data, &token); err != nil {
		return nil, fmt.Errorf("115: 解析 get_token 结果失败：%w", err)
	}
	token.normalize()
	return &token, nil
}

// Upload 上传单个本地文件到 115 指定父目录（cid），返回成功后的远端文件信息。
// filePath 必须是落到磁盘的真实文件路径（调用方负责把 io.Reader 落盘为临时文件）。
func (c *OpenClient) Upload(ctx context.Context, filePath, parentCID, signKey, signVal string) (*UploadCompleteResult, error) {
	fileSize := fileSizeOf(filePath)
	if fileSize < 0 {
		return nil, fmt.Errorf("115: 无法获取文件大小：%s", filePath)
	}
	fileSha1, err := FileSHA1(filePath)
	if err != nil {
		return nil, fmt.Errorf("115: 计算文件 SHA1 失败：%w", err)
	}
	preSha1, err := FileSHA1Partial(filePath, 0, 128*1024-1)
	if err != nil {
		return nil, fmt.Errorf("115: 计算文件前 128 KiB SHA1 失败：%w", err)
	}
	request := UploadInitRequest{
		FileName:     baseNameOf(filePath),
		FileSize:     fileSize,
		ParentFileId: parentCID,
		FileSha1:     fileSha1,
		Preid:        preSha1,
		TopUpload:    "0",
		SignKey:      signKey,
		SignVal:      signVal,
	}
	initResult, err := c.UploadInit(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("115: 上传初始化失败：%w", err)
	}
	status := initResult.Status
	if status == UploadInitStatusNeedSign {
		// 二次签名：按 sign_check 指定区间重算 sha1
		rng, err := parseSignCheckRange(initResult.SignCheck)
		if err != nil {
			return nil, err
		}
		signValue, err := FileSHA1Partial(filePath, rng.Start, rng.End)
		if err != nil {
			return nil, err
		}
		request.SignKey = initResult.SignKey
		request.SignVal = signValue
		initResult, err = c.UploadInit(ctx, request)
		if err != nil {
			return nil, fmt.Errorf("115: 上传二次签名失败：%w", err)
		}
		status = initResult.Status
	}
	switch status {
	case UploadInitStatusRapidUploaded:
		// 秒传成功：必须带远端文件定位信息，否则视为异常响应
		if initResult.FileId == "" || initResult.PickCode == "" {
			return nil, fmt.Errorf("115: 秒传成功但缺少 file_id/pick_code（status=%d）", status)
		}
		return &UploadCompleteResult{FileId: initResult.FileId, PickCode: initResult.PickCode}, nil
	case UploadInitStatusSignFailed:
		return nil, fmt.Errorf("115: 签名验证后失败")
	case UploadInitStatusSignRejected:
		return nil, fmt.Errorf("115: 签名认证失败")
	case UploadInitStatusNeedUpload:
		// 真实上传：OSS multipart
	default:
		// 未知状态不能当成功返回（会静默丢文件），显式报错便于排查
		return nil, fmt.Errorf("115: 未知的 upload/init 状态 %d", status)
	}

	if initResult.Bucket == "" || initResult.Object == "" {
		return nil, fmt.Errorf("115: upload/init 缺少 bucket/object 信息")
	}
	token, err := c.GetUploadToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("115: 获取上传凭证失败：%w", err)
	}
	if token == nil || token.Endpoint == "" || token.AccessKeyId == "" || token.AccessKeySecret == "" {
		return nil, fmt.Errorf("115: 上传凭证不完整")
	}
	uploader := NewOSSMultipartUploader(token.Endpoint, token.AccessKeyId, token.AccessKeySecret, token.SecurityToken)
	result, err := uploader.UploadFile(ctx, OSSMultipartUploadInput{
		Bucket:      initResult.Bucket,
		Object:      initResult.Object,
		Callback:    initResult.Callback.Callback,
		CallbackVar: initResult.Callback.CallbackVar,
		FilePath:    filePath,
		FileSize:    fileSize,
		refreshClient: func(ctx context.Context) (ossMultipartClient, error) {
			refreshed, rerr := c.GetUploadToken(ctx)
			if rerr != nil || refreshed == nil {
				return nil, rerr
			}
			return newOSSMultipartClient(refreshed.Endpoint, refreshed.AccessKeyId, refreshed.AccessKeySecret, refreshed.SecurityToken), nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("115: OSS 上传失败：%w", err)
	}
	complete, err := ParseCompleteCallbackResult(result)
	if err != nil {
		return nil, err
	}
	return &complete, nil
}

// MkDir 在 115 的 parentCid 下创建目录，返回新目录 cid。
func (c *OpenClient) MkDir(ctx context.Context, parentCID, name string) (string, error) {
	params := map[string]string{
		"cname": name,
		"pid":   parentCID,
	}
	resp, err := c.doAuthJSON(ctx, "POST", ProAPIBase+"/open/folder/add", params, 2)
	if err != nil {
		return "", err
	}
	// /open/folder/add 结构：{ aid, cid, fid, name, pid, ... }，单一对象
	var r struct {
		Cid string `json:"cid"`
	}
	if err := json.Unmarshal(resp.Data, &r); err != nil {
		return "", fmt.Errorf("115: 解析 folder/add 结果失败：%w", err)
	}
	if r.Cid == "" {
		return "", errors.New("115: folder/add 未返回 cid")
	}
	return r.Cid, nil
}

func fileSizeOf(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return -1
	}
	if info.IsDir() {
		return -1
	}
	return info.Size()
}

func baseNameOf(path string) string {
	s := path
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' || s[i] == '\\' {
			return s[i+1:]
		}
	}
	return s
}
