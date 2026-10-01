package cloud115

import (
	"crypto/sha1"
	"encoding/hex"
	"io"
	"os"
	"strings"
)

// FileSHA1 计算文件完整 SHA1（大写 hex，115 全链路统一大写）。
func FileSHA1(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(h.Sum(nil))), nil
}

// FileSHA1Partial 计算文件 [start,end]（含）字节区间的 SHA1（大写 hex）。
// 用于 115 上传二次签名按 sign_check 指定的区间重算哈希；sign_val 必须为大写，
// 否则 115 以 status=8「签名认证失败」拒绝。
func FileSHA1Partial(path string, start, end int64) (string, error) {
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	length := end - start + 1
	h := sha1.New()
	// io.CopyN 在文件不足 length 字节时会返回 io.EOF，导致小文件（如小于 128 KiB 的
	// 元数据图片）无法上传。这里只拷贝实际读到的字节，文件尾对齐到区间终点即可。
	if _, err := io.CopyN(h, f, length); err != nil && err != io.EOF {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(h.Sum(nil))), nil
}
