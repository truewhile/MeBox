// 115 开放平台（openapi）驱动：替代原 cookie 逆向方案。
//
// 账号配置（StrmAccount.Config JSON）：
//
//	{
//	  "app_id":         "100195125",      // 开放平台应用 ID
//	  "access_token":   "...",            // 加密存储
//	  "refresh_token":  "...",            // 加密存储
//	  "user_id":        "12345",          // 可选
//	  "user_name":      "user"            // 可选
//	}
//
// 授权流程：设备码扫码（官方 PKCE 应用目录）、QMediaSync/MQFamily 中继、
// MoviePilot 轮询、CloudDrive 回跳，见 internal/service/cloud115。
package cloud

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/truewhile/MeBox/internal/service/cloud115"
)

// OpenAPI115Provider 暴露 115 开放平台驱动接口。
type OpenAPI115Provider interface {
	Provider
	OpenClient() *cloud115.OpenClient
}

// openAPI115Provider 实现 Provider 接口：List 列目录、Resolve 用 pickcode
// 换下载直链（302 offload，无需代理）、Ping 探测根目录。
type openAPI115Provider struct {
	c *cloud115.OpenClient
}

// NewOpenAPI115 构造 115 开放平台驱动。
func NewOpenAPI115(appID, accessToken, refreshToken string) *openAPI115Provider {
	return &openAPI115Provider{c: cloud115.NewOpenClient(strings.TrimSpace(appID), accessToken, refreshToken)}
}

func (p *openAPI115Provider) Type() string { return Type115 }

func (p *openAPI115Provider) Ping(ctx context.Context) error {
	if strings.TrimSpace(p.c.AppID) == "" {
		return fmt.Errorf("115: 缺少开放平台应用 ID，请重新授权")
	}
	if strings.TrimSpace(p.c.CurrentAccessToken()) == "" {
		return fmt.Errorf("115: 缺少访问令牌，请重新授权")
	}
	_, _, err := p.c.GetFsList(ctx, "0", 0, 1)
	return err
}

func (p *openAPI115Provider) List(ctx context.Context, dirID string) ([]FileEntry, error) {
	// 115 开放平台列表接口按 offset/limit 分页，这里循环取完整个目录；
	// limit 上限 1150（官方文档《获取文件列表》），取上限减少大目录翻页次数
	const pageSize = 1150
	var out []FileEntry
	for offset := 0; ; offset += pageSize {
		files, _, err := p.c.GetFsList(ctx, dirID, offset, pageSize)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			out = append(out, FileEntry{
				ID:       f.FileId,
				Name:     f.FileName,
				IsDir:    f.Category == cloud115.TypeDir,
				Size:     f.FileSize,
				MTime:    f.ModifiedAt(),
				PickCode: f.PickCode,
				Sha1:     f.Sha1,
			})
		}
		if len(files) < pageSize {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
	return out, nil
}

func (p *openAPI115Provider) Resolve(ctx context.Context, fileRef string) (*DirectLink, error) {
	return p.ResolveWithUA(ctx, fileRef, "")
}

func (p *openAPI115Provider) ResolveWithUA(ctx context.Context, fileRef, ua string) (*DirectLink, error) {
	url, err := p.c.GetDownloadURLWithUA(ctx, fileRef, ua)
	if err != nil {
		return nil, err
	}
	// 115 CDN 防盗链白名单：直链绑定换取时的 User-Agent（调用方 UA 或
	// DefaultUA），后续请求必须携带同一 UA，否则 403。302 播放由客户端
	// 自带 UA 天然满足；服务端直连（弹幕 hash 等）依赖这里的 Headers。
	bound := strings.TrimSpace(ua)
	if bound == "" {
		bound = cloud115.DefaultUA
	}
	return &DirectLink{URL: url, Proxy: false, Headers: map[string]string{"User-Agent": bound}}, nil
}

// ResolveBatch 批量换取直链（downurl 支持逗号分隔多 pick_code，一次请求覆盖
// 整批下载任务的换链）。返回 pickcode → 直链，未解析成功的引用不在结果中；
// err 非 nil 表示批量过程部分/全部失败，调用方对缺失项回退到逐个 Resolve。
// 下载队列统一使用默认 UA，与单个换取的防盗链绑定语义一致。
func (p *openAPI115Provider) ResolveBatch(ctx context.Context, fileRefs []string) (map[string]*DirectLink, error) {
	urls, err := p.c.GetDownloadURLsBatch(ctx, fileRefs, "")
	out := make(map[string]*DirectLink, len(urls))
	for pc, u := range urls {
		out[pc] = &DirectLink{URL: u, Proxy: false, Headers: map[string]string{"User-Agent": cloud115.DefaultUA}}
	}
	return out, err
}

// OpenClient 暴露底层客户端（token 刷新用）。
func (p *openAPI115Provider) OpenClient() *cloud115.OpenClient { return p.c }

// PutLocalFile 直接上传本地文件，避免通过 io.Reader 复制临时文件产生的磁盘开销与并发重命名碰撞。
func (p *openAPI115Provider) PutLocalFile(ctx context.Context, parentCID, localPath string) error {
	_, err := p.c.Upload(ctx, localPath, parentCID, "", "")
	return err
}

// PutFileNamed 把本地元数据上传到 115 指定父目录（parentCID 为父目录 cid）。
// 为防止多并发上传线程在同一临时目录下发生同名文件（如 poster.jpg）碰撞覆盖与误删，
// 为每个上传任务分配专属临时子目录。
func (p *openAPI115Provider) PutFileNamed(ctx context.Context, parentCID, fileName string, r io.Reader) error {
	tmpDir, err := os.MkdirTemp("", "mebox-upload-*")
	if err != nil {
		return fmt.Errorf("115: 创建临时目录失败：%w", err)
	}
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()

	safeName := filepath.Base(fileName)
	if safeName == "" || safeName == "." {
		safeName = "file"
	}
	tmpPath := filepath.Join(tmpDir, safeName)
	dst, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("115: 创建临时文件失败：%w", err)
	}
	if _, err := io.Copy(dst, r); err != nil {
		_ = dst.Close()
		return fmt.Errorf("115: 写入临时文件失败：%w", err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("115: 关闭临时文件失败：%w", err)
	}

	_, err = p.c.Upload(ctx, tmpPath, parentCID, "", "")
	if err != nil {
		return err
	}
	return nil
}

// RefreshToken 刷新访问令牌并返回新令牌；refresh_token 失效时返回
// cloud115.IsRefreshTokenDead(err) 为 true 的错误。
func (p *openAPI115Provider) RefreshToken(refreshToken string) (*cloud115.TokenData, error) {
	return p.c.RefreshToken(refreshToken)
}
