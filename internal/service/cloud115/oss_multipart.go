// 阿里云 OSS multipart 分片上传（用于 115 元数据上传直传）。
// 使用 115 下发的临时 STS 凭证，将本地文件分片上传到 OSS，并经 complete 回调
// 通知 115 完成落盘。参考 QMediaSync 的 OSSMultipartUploader 实现。
package cloud115

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sort"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
)

const (
	defaultMultipartPartSize int64 = 32 * 1024 * 1024
	multipartPartAlign       int64 = 1024 * 1024
	maxMultipartParts        int64 = 9999
	maxMultipartPartSize     int64 = 5 * 1024 * 1024 * 1024
)

type ossMultipartClient interface {
	InitiateMultipartUpload(context.Context, *oss.InitiateMultipartUploadRequest, ...func(*oss.Options)) (*oss.InitiateMultipartUploadResult, error)
	UploadPart(context.Context, *oss.UploadPartRequest, ...func(*oss.Options)) (*oss.UploadPartResult, error)
	ListParts(context.Context, *oss.ListPartsRequest, ...func(*oss.Options)) (*oss.ListPartsResult, error)
	CompleteMultipartUpload(context.Context, *oss.CompleteMultipartUploadRequest, ...func(*oss.Options)) (*oss.CompleteMultipartUploadResult, error)
	AbortMultipartUpload(context.Context, *oss.AbortMultipartUploadRequest, ...func(*oss.Options)) (*oss.AbortMultipartUploadResult, error)
}

// OSSMultipartUploader 封装 OSS multipart 上传。
type OSSMultipartUploader struct {
	client ossMultipartClient
}

// OSSMultipartUploadInput 是 multipart 上传输入。
type OSSMultipartUploadInput struct {
	Bucket        string
	Object        string
	Callback      string
	CallbackVar   string
	FilePath      string
	FileSize      int64
	UploadId      string
	PartSize      int64
	PartRetryMax  int
	refreshClient func(context.Context) (ossMultipartClient, error)
}

// OSSMultipartUploadResult 是 multipart 上传后的结果。
type OSSMultipartUploadResult struct {
	CallbackResult map[string]any
	UploadId       string
	PartSize       int64
	TotalParts     int
	UploadedBytes  int64
	UploadedParts  int
}

// CalculateMultipartPartSize 计算 OSS multipart 分片大小与分片数量。
func CalculateMultipartPartSize(fileSize int64) (int64, int, error) {
	if fileSize < 0 {
		return 0, 0, fmt.Errorf("文件大小不能为负数：%d", fileSize)
	}
	partSize := defaultMultipartPartSize
	minPartSize := ceilDiv(fileSize, maxMultipartParts)
	if minPartSize > partSize {
		partSize = roundUp(minPartSize, multipartPartAlign)
	}
	if partSize > maxMultipartPartSize {
		return 0, 0, fmt.Errorf("文件过大，所需分片大小 %d 超过 OSS 上限 %d", partSize, maxMultipartPartSize)
	}
	totalParts := int(ceilDiv(fileSize, partSize))
	if totalParts == 0 {
		totalParts = 1
	}
	if int64(totalParts) > maxMultipartParts {
		return 0, 0, fmt.Errorf("分片数量 %d 超过上限 %d", totalParts, maxMultipartParts)
	}
	return partSize, totalParts, nil
}

// NewOSSMultipartUploader 创建 OSS multipart 上传器。
func NewOSSMultipartUploader(endpoint, accessKeyId, accessKeySecret, securityToken string) *OSSMultipartUploader {
	return &OSSMultipartUploader{client: newOSSMultipartClient(endpoint, accessKeyId, accessKeySecret, securityToken)}
}

func newOSSMultipartClient(endpoint, accessKeyId, accessKeySecret, securityToken string) ossMultipartClient {
	cfg := oss.LoadDefaultConfig().
		WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyId, accessKeySecret, securityToken)).
		WithRegion("cn-shenzhen").
		WithEndpoint(endpoint)
	return oss.NewClient(cfg)
}

// UploadFile 上传文件并完成 OSS multipart，返回 complete callback 结果。
func (u *OSSMultipartUploader) UploadFile(ctx context.Context, input OSSMultipartUploadInput) (map[string]any, error) {
	result, err := u.UploadFileWithResult(ctx, input)
	if err != nil {
		return nil, err
	}
	return result.CallbackResult, nil
}

// UploadedPart 是 OSS 已上传分片的定位信息（断点续传时复用 ETag 用）。
type UploadedPart struct {
	PartNumber int32
	Size       int64
	ETag       string
}

// UploadFileWithResult 上传文件并返回 multipart 结果。
// 任一失败路径（分片上传失败 / callback 校验失败 / Complete 失败 / 文件打开失败等）
// 都会经 defer 统一 AbortMultipartUpload 丢弃本次 Initiate 出的 multipart
// （abort 失败仅记日志），避免 OSS 分片永久泄漏；成功路径不 Abort。
func (u *OSSMultipartUploader) UploadFileWithResult(ctx context.Context, input OSSMultipartUploadInput) (result OSSMultipartUploadResult, err error) {
	if input.PartRetryMax <= 0 {
		input.PartRetryMax = 3
	}
	partSize := input.PartSize
	totalParts := 0
	if partSize <= 0 {
		partSize, totalParts, err = CalculateMultipartPartSize(input.FileSize)
		if err != nil {
			return OSSMultipartUploadResult{}, err
		}
	} else {
		totalParts = int(ceilDiv(input.FileSize, partSize))
	}

	uploadId := input.UploadId
	// ownUploadId 标记 uploadId 是否为本调用 Initiate 出来的：仅自建的
	// multipart 在失败时由本函数 Abort；调用方显式传入的 uploadId（断点续传）
	// 失败后保留现场，由调用方决定重试或清理。
	ownUploadId := uploadId == ""
	if ownUploadId {
		initResult, initErr := u.client.InitiateMultipartUpload(ctx, &oss.InitiateMultipartUploadRequest{
			Bucket: oss.Ptr(input.Bucket),
			Key:    oss.Ptr(input.Object),
			RequestCommon: oss.RequestCommon{
				Parameters: map[string]string{"sequential": "1"},
			},
		})
		if initErr != nil {
			return OSSMultipartUploadResult{}, fmt.Errorf("初始化 OSS multipart 失败：%w", initErr)
		}
		if initResult.UploadId == nil || *initResult.UploadId == "" {
			return OSSMultipartUploadResult{}, fmt.Errorf("初始化 OSS multipart 返回空 upload_id")
		}
		uploadId = *initResult.UploadId
	}
	defer func() {
		if err == nil || !ownUploadId || uploadId == "" {
			return
		}
		// 失败路径统一 Abort 丢弃已上传分片；ctx 可能已取消，脱离其取消信号尽力清理
		abortCtx := context.WithoutCancel(ctx)
		if _, abortErr := u.client.AbortMultipartUpload(abortCtx, &oss.AbortMultipartUploadRequest{
			Bucket:   oss.Ptr(input.Bucket),
			Key:      oss.Ptr(input.Object),
			UploadId: oss.Ptr(uploadId),
		}); abortErr != nil {
			log.Printf("115: 中止 OSS multipart 失败（upload_id=%s，可能残留分片）：%v", uploadId, abortErr)
		}
	}()

	existingPartMap := make(map[int32]UploadedPart)
	if existingParts, listErr := u.ListUploadedParts(ctx, input.Bucket, input.Object, uploadId); listErr == nil {
		for _, part := range existingParts {
			existingPartMap[part.PartNumber] = part
		}
	}

	file, err := os.Open(input.FilePath)
	if err != nil {
		return OSSMultipartUploadResult{}, fmt.Errorf("打开待上传文件失败：%w", err)
	}
	defer file.Close()

	var uploadedBytes int64
	uploadedParts := 0
	completeParts := make([]oss.UploadPart, 0, totalParts)
	for partNumber := 1; partNumber <= totalParts; partNumber++ {
		offset := int64(partNumber-1) * partSize
		length := minInt64(partSize, input.FileSize-offset)
		if length < 0 {
			length = 0
		}
		// 断点续传：分片已完整上传（大小一致即代表分片大小未变）时直接复用
		// ListParts 返回的 ETag，跳过重传，也不再重复累加统计
		if existing, ok := existingPartMap[int32(partNumber)]; ok && existing.Size == length && existing.ETag != "" {
			uploadedBytes += length
			uploadedParts++
			completeParts = append(completeParts, oss.UploadPart{
				PartNumber: int32(partNumber),
				ETag:       oss.Ptr(existing.ETag),
			})
			continue
		}
		etag, uploadErr := u.uploadPartWithRetry(ctx, input, uploadId, int32(partNumber), file, offset, length)
		if uploadErr != nil {
			return OSSMultipartUploadResult{}, uploadErr
		}
		uploadedBytes += length
		uploadedParts++
		completeParts = append(completeParts, oss.UploadPart{
			PartNumber: int32(partNumber),
			ETag:       oss.Ptr(etag),
		})
	}
	sort.Slice(completeParts, func(i, j int) bool {
		return completeParts[i].PartNumber < completeParts[j].PartNumber
	})

	// 115 下发的 callback / callback_var 是 JSON 字符串，而 OSS CompleteMultipartUpload
	// 要求 callback 参数为 Base64 编码后的 JSON，否则报 "The callback configuration is
	// not base64 encoded"。这里把两者转为 Base64 后再提交（参考 QMediaSync 的
	// BuildOSSCallbackHeaders）。
	cb := input.Callback
	cbVar := input.CallbackVar
	if cb == "" {
		return OSSMultipartUploadResult{}, errors.New("OSS callback 为空")
	}
	if !json.Valid([]byte(cb)) {
		return OSSMultipartUploadResult{}, errors.New("解析 callback 失败：不是合法 JSON")
	}
	if cbVar == "" {
		cbVar = "{}"
	}
	if !json.Valid([]byte(cbVar)) {
		return OSSMultipartUploadResult{}, errors.New("解析 callback_var 失败：不是合法 JSON")
	}
	completeResult, err := u.client.CompleteMultipartUpload(ctx, &oss.CompleteMultipartUploadRequest{
		Bucket:   oss.Ptr(input.Bucket),
		Key:      oss.Ptr(input.Object),
		UploadId: oss.Ptr(uploadId),
		CompleteMultipartUpload: &oss.CompleteMultipartUpload{
			Parts: completeParts,
		},
		Callback:    oss.Ptr(base64.StdEncoding.EncodeToString([]byte(cb))),
		CallbackVar: oss.Ptr(base64.StdEncoding.EncodeToString([]byte(cbVar))),
	})
	if err != nil {
		return OSSMultipartUploadResult{}, fmt.Errorf("完成 OSS multipart 失败：%w", err)
	}
	return OSSMultipartUploadResult{
		CallbackResult: completeResult.CallbackResult,
		UploadId:       uploadId,
		PartSize:       partSize,
		TotalParts:     totalParts,
		UploadedBytes:  uploadedBytes,
		UploadedParts:  uploadedParts,
	}, nil
}

// ListUploadedParts 查询 OSS 已上传分片（MaxParts 上限 1000，超过时按
// NextPartNumberMarker 自动翻页取全量，否则断点续传只能看到前 1000 片）。
func (u *OSSMultipartUploader) ListUploadedParts(ctx context.Context, bucket, object, uploadId string) ([]UploadedPart, error) {
	parts := []UploadedPart{}
	var marker int32
	for {
		result, err := u.client.ListParts(ctx, &oss.ListPartsRequest{
			Bucket:           oss.Ptr(bucket),
			Key:              oss.Ptr(object),
			UploadId:         oss.Ptr(uploadId),
			MaxParts:         1000,
			PartNumberMarker: marker,
		})
		if err != nil {
			return nil, fmt.Errorf("查询 OSS 已上传分片失败：%w", err)
		}
		for _, part := range result.Parts {
			etag := ""
			if part.ETag != nil {
				etag = *part.ETag
			}
			parts = append(parts, UploadedPart{PartNumber: part.PartNumber, Size: part.Size, ETag: etag})
		}
		if !result.IsTruncated || result.NextPartNumberMarker <= marker {
			// 防御：marker 不前进时终止循环，避免异常响应导致死循环
			break
		}
		marker = result.NextPartNumberMarker
	}
	return parts, nil
}

func (u *OSSMultipartUploader) uploadPartWithRetry(
	ctx context.Context,
	input OSSMultipartUploadInput,
	uploadId string,
	partNumber int32,
	file *os.File,
	offset, length int64,
) (string, error) {
	var lastErr error
	for attempt := 0; attempt < input.PartRetryMax; attempt++ {
		reader := io.NewSectionReader(file, offset, length)
		result, err := u.client.UploadPart(ctx, &oss.UploadPartRequest{
			Bucket:        oss.Ptr(input.Bucket),
			Key:           oss.Ptr(input.Object),
			PartNumber:    partNumber,
			UploadId:      oss.Ptr(uploadId),
			Body:          reader,
			ContentLength: oss.Ptr(length),
		})
		if err == nil {
			if result.ETag == nil || *result.ETag == "" {
				return "", fmt.Errorf("OSS part %d 返回空 ETag", partNumber)
			}
			return *result.ETag, nil
		}
		lastErr = err
		if attempt < input.PartRetryMax-1 && input.refreshClient != nil {
			refreshed, refreshErr := input.refreshClient(ctx)
			if refreshErr != nil {
				lastErr = refreshErr
				continue
			}
			u.client = refreshed
		}
	}
	return "", fmt.Errorf("上传 OSS part %d 失败：%w", partNumber, lastErr)
}

// ParseCompleteCallbackResult 校验并解析 OSS complete 后的 115 callback 结果。
func ParseCompleteCallbackResult(result map[string]any) (UploadCompleteResult, error) {
	if result == nil {
		return UploadCompleteResult{}, errors.New("OSS complete callback 结果为空")
	}
	if state, ok := result["state"].(bool); ok && !state {
		return UploadCompleteResult{}, fmt.Errorf("115 callback 返回失败：%s", anyToString(result["message"]))
	}
	if message := anyToString(result["message"]); message != "" {
		return UploadCompleteResult{}, fmt.Errorf("115 callback 返回错误：%s", message)
	}
	data, ok := result["data"].(map[string]any)
	if !ok {
		return UploadCompleteResult{}, errors.New("115 callback 缺少 data")
	}
	complete := UploadCompleteResult{
		FileId:   anyToString(data["file_id"]),
		PickCode: anyToString(data["pick_code"]),
		ParentId: anyToString(data["parent_id"]),
		Sha1:     anyToString(data["sha1"]),
		Size:     anyToInt64(data["size"]),
		Mtime:    anyToInt64(data["mtime"]),
	}
	if complete.FileId == "" || complete.PickCode == "" {
		return UploadCompleteResult{}, errors.New("115 callback 缺少 file_id/pick_code")
	}
	return complete, nil
}

func ceilDiv(n, d int64) int64 {
	if d <= 0 {
		return 0
	}
	if n <= 0 {
		return 0
	}
	return (n + d - 1) / d
}

func roundUp(n, align int64) int64 {
	if align <= 0 {
		return n
	}
	return ceilDiv(n, align) * align
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func anyToInt64(v any) int64 {
	switch t := v.(type) {
	case string:
		var n int64
		fmt.Sscanf(t, "%d", &n)
		return n
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	default:
		return 0
	}
}

func anyToString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
