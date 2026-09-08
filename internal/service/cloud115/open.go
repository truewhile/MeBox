// 115 开放平台只读 API：列目录、详情、下载直链、授权（二维码/换 token/刷新）。
package cloud115

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ─── 文件模型 ──────────────────────────────────────────────────────────────────

type FileType string

const (
	TypeFile FileType = "1"
	TypeDir  FileType = "0"
)

// RemoteFile 是 115 文件列表中的一个条目。
type RemoteFile struct {
	FileId   string   `json:"fid"`  // 文件 ID
	Pid      string   `json:"pid"`  // 父文件夹 ID
	Category FileType `json:"fc"`   // 0 文件夹 1 文件
	FileName string   `json:"fn"`   // 文件名
	PickCode string   `json:"pc"`   // 提取码
	Utime    int64    `json:"upt"`  // 修改时间
	Ptime    int64    `json:"uppt"` // 上传时间
	Sha1     string   `json:"sha1"` // SHA1
	FileSize int64    `json:"fs"`   // 大小
	Fta      string   `json:"fta"`  // 0/2 未上传完成，1 已完成
}

func (f RemoteFile) ModifiedAt() int64 {
	if f.Utime > 0 {
		return f.Utime
	}
	return f.Ptime
}

type FileListResp struct {
	RespBase
	Path []struct {
		Name   string `json:"name"`
		FileId string `json:"cid"`
	} `json:"path"`
	PathStr string `json:"path_str"`
}

// GetFsList 列目录（cid=0 为根目录）。
func (c *OpenClient) GetFsList(ctx context.Context, cid string, offset, limit int) ([]RemoteFile, string, error) {
	if cid == "" {
		cid = "0"
	}
	params := map[string]string{"cid": cid}
	if limit > 0 {
		params["limit"] = fmt.Sprint(limit)
	}
	if offset > 0 {
		params["offset"] = fmt.Sprint(offset)
	}
	params["cur"] = "1"
	params["show_dir"] = "1"
	resp, err := c.doAuthJSON(ctx, "GET", ProAPIBase+"/open/ufile/files", params, 2)
	if err != nil {
		return nil, "", err
	}
	// state=false（token 过期、限流、业务失败等）绝不能当作空目录返回：
	// 同步流程会据此认为远端已清空并清理本地 .strm/元数据文件。
	if !resp.State {
		return nil, "", NewOpenAPIResponseError(resp.Code, resp.Errno, resp.Message, resp.Error, "115 接口调用失败")
	}
	var list FileListResp
	list.RespBase = *resp
	if len(resp.Raw) > 0 {
		_ = json.Unmarshal(resp.Raw, &list)
	}
	files, err := openList[RemoteFile](resp.Data)
	if err != nil {
		return nil, "", fmt.Errorf("115: 解析文件列表失败：%w", err)
	}
	pathStr := make([]string, 0, len(list.Path))
	for _, item := range list.Path {
		if item.FileId == "0" {
			continue
		}
		pathStr = append(pathStr, item.Name)
	}
	return files, strings.Join(pathStr, "/"), nil
}

// GetFsListFlat 递归扁平化列出 cid 下的所有文件（跨越所有子目录，不包含文件夹节点），并返回文件列表与该树下的总文件数。
// 类似于 QMediaSync 的 115 扁平化批量拉取机制，极大地降低多层级子目录下的 API 请求次数。
func (c *OpenClient) GetFsListFlat(ctx context.Context, cid string, offset, limit int) ([]RemoteFile, int64, error) {
	if cid == "" {
		cid = "0"
	}
	if limit <= 0 {
		limit = 1150
	}
	params := map[string]string{
		"cid":      cid,
		"limit":    fmt.Sprint(limit),
		"offset":   fmt.Sprint(offset),
		"cur":      "0",
		"show_dir": "0",
	}
	resp, err := c.doAuthJSON(ctx, "GET", ProAPIBase+"/open/ufile/files", params, 2)
	if err != nil {
		return nil, 0, err
	}
	if !resp.State {
		return nil, 0, NewOpenAPIResponseError(resp.Code, resp.Errno, resp.Message, resp.Error, "115 接口调用失败")
	}
	files, err := openList[RemoteFile](resp.Data)
	if err != nil {
		return nil, 0, fmt.Errorf("115: 解析文件列表失败：%w", err)
	}
	return files, resp.Count, nil
}

// FindNamedContentInParent 在父目录下查找与本地内容一致的同名文件。
// 匹配条件：文件名完全一致、大小一致，且 SHA1 为空或与 expectedSHA1 大小写不敏感相等。
// 同时返回该目录下全部同名文件（含未匹配的脏副本），便于上传前清理。
// 目录过大时分页扫描，最多拉取 maxListPages 页（每页 pageSize 条）。
func (c *OpenClient) FindNamedContentInParent(ctx context.Context, parentCID, fileName, expectedSHA1 string, expectedSize int64) (matched *RemoteFile, sameName []RemoteFile, err error) {
	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		return nil, nil, nil
	}
	const pageSize = 200
	const maxListPages = 20 // 最多扫描 4000 项，元数据父目录通常远小于此
	expectedSHA1 = strings.TrimSpace(expectedSHA1)
	offset := 0
	for page := 0; page < maxListPages; page++ {
		files, _, listErr := c.GetFsList(ctx, parentCID, offset, pageSize)
		if listErr != nil {
			return nil, sameName, listErr
		}
		if len(files) == 0 {
			break
		}
		for i := range files {
			f := files[i]
			if f.Category == TypeDir {
				continue
			}
			// fta=0/2 表示未上传完成，不可作为已存在副本
			if f.Fta == "0" || f.Fta == "2" {
				continue
			}
			if f.FileName != fileName {
				continue
			}
			sameName = append(sameName, f)
			if matched != nil {
				continue
			}
			if f.FileSize != expectedSize {
				continue
			}
			remoteSha := strings.TrimSpace(f.Sha1)
			if remoteSha == "" || remoteSha == "-" || strings.EqualFold(remoteSha, expectedSHA1) {
				cp := f
				matched = &cp
			}
		}
		if len(files) < pageSize {
			break
		}
		offset += len(files)
	}
	return matched, sameName, nil
}

// GetFsDetailByCid 查询文件（夹）详情。
func (c *OpenClient) GetFsDetailByCid(ctx context.Context, fileId string) (*RemoteFileDetail, error) {
	params := map[string]string{"file_id": fileId}
	resp, err := c.doAuthJSON(ctx, "GET", ProAPIBase+"/open/folder/get_info", params, 2)
	if err != nil {
		return nil, err
	}
	return openFirstList[RemoteFileDetail](resp.Data)
}

// RemoteFileDetail 是文件详情。
type RemoteFileDetail struct {
	FileId   string   `json:"file_id"`
	FileName string   `json:"file_name"`
	PickCode string   `json:"pick_code"`
	Sha1     string   `json:"sha1"`
	Category FileType `json:"file_category"`
	SizeByte int64    `json:"size_byte"`
	Paths    []struct {
		FileId string `json:"file_id"`
		Name   string `json:"file_name"`
	} `json:"paths"`
}

// RelativePath 计算该目录相对于根同步目录（rootCID）的相对路径。
func (d *RemoteFileDetail) RelativePath(rootCID string) string {
	if d == nil {
		return ""
	}
	if rootCID == "" {
		rootCID = "0"
	}
	if d.FileId == rootCID {
		return ""
	}
	rootIdx := -1
	for i, p := range d.Paths {
		if p.FileId == rootCID {
			rootIdx = i
			break
		}
	}
	var segments []string
	start := 0
	if rootIdx >= 0 {
		start = rootIdx + 1
	} else if len(d.Paths) > 0 && (d.Paths[0].FileId == "0" || d.Paths[0].FileId == "") {
		start = 1
	}
	hasSelf := false
	for i := start; i < len(d.Paths); i++ {
		if d.Paths[i].FileId == d.FileId {
			hasSelf = true
		}
		name := strings.TrimSpace(d.Paths[i].Name)
		if name != "" {
			segments = append(segments, name)
		}
	}
	// 若 115 返回的 paths 祖先链未包含当前目录自身，则将其自身目录名 FileName 补在末尾
	if !hasSelf && strings.TrimSpace(d.FileName) != "" && d.FileId != rootCID {
		segments = append(segments, strings.TrimSpace(d.FileName))
	}
	return strings.Join(segments, "/")
}

// ─── 下载直链 ──────────────────────────────────────────────────────────────────

type downloadURLData struct {
	FileName string `json:"file_name"`
	PickCode string `json:"pick_code"`
	Sha1     string `json:"sha1"`
	URL      struct {
		URL string `json:"url"`
	} `json:"url"`
}

// GetDownloadURL 获取下载直链（pickcode）。命中缓存直接返回，
// 避免对同一文件反复换取直链触发 115 风控。
func (c *OpenClient) GetDownloadURL(ctx context.Context, pickCode string) (string, error) {
	return c.GetDownloadURLWithUA(ctx, pickCode, "")
}

// GetDownloadURLWithUA 支持按调用方/播放器 User-Agent 换取对应的 115 CDN 直链（用于 115 防盗链白名单校验）。
func (c *OpenClient) GetDownloadURLWithUA(ctx context.Context, pickCode, ua string) (string, error) {
	ua = strings.TrimSpace(ua)
	if cached := GetDownloadURLCache(pickCode, ua); cached != "" {
		return cached, nil
	}
	params := map[string]string{"pick_code": pickCode}
	resp, err := c.doAuthJSONWithUA(ctx, "POST", ProAPIBase+"/open/ufile/downurl", params, 1, ua)
	if err != nil {
		return "", err
	}
	var data map[string]downloadURLData
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return "", fmt.Errorf("115: 解析下载地址失败：%w", err)
	}
	first := firstOrEmpty(data)
	if first.URL.URL == "" {
		return "", fmt.Errorf("115: 下载地址为空（文件可能未上传完成或已被删除）")
	}
	SetDownloadURLCache(pickCode, first.URL.URL, ua)
	return first.URL.URL, nil
}

// downurlBatchSize 单次批量换取直链的 pick_code 数上限。官方 /open/ufile/downurl
// 支持逗号分隔多个 pick_code，批量可大幅降低元数据下载的换链请求量；大小取
// 保守值，减小单个违规/异常文件导致整批失败的爆炸半径。
const downurlBatchSize = 10

// GetDownloadURLsBatch 批量获取下载直链（pickcode → URL）。先查进程内缓存，
// 仅对未命中的 pick_code 分片发起批量请求；单个分片失败时返回已解析的部分与
// 错误，调用方对缺失项回退到逐个 GetDownloadURLWithUA。UA 语义与单个换取
// 一致：直链绑定换取时的 UA，后续下载必须携带同一 UA。
func (c *OpenClient) GetDownloadURLsBatch(ctx context.Context, pickCodes []string, ua string) (map[string]string, error) {
	ua = strings.TrimSpace(ua)
	out := make(map[string]string, len(pickCodes))
	seen := make(map[string]struct{}, len(pickCodes))
	missing := make([]string, 0, len(pickCodes))
	for _, pc := range pickCodes {
		pc = strings.TrimSpace(pc)
		if pc == "" {
			continue
		}
		if _, dup := seen[pc]; dup {
			continue
		}
		seen[pc] = struct{}{}
		if cached := GetDownloadURLCache(pc, ua); cached != "" {
			out[pc] = cached
			continue
		}
		missing = append(missing, pc)
	}
	for start := 0; start < len(missing); start += downurlBatchSize {
		end := start + downurlBatchSize
		if end > len(missing) {
			end = len(missing)
		}
		chunk := missing[start:end]
		params := map[string]string{"pick_code": strings.Join(chunk, ",")}
		resp, err := c.doAuthJSONWithUA(ctx, "POST", ProAPIBase+"/open/ufile/downurl", params, 1, ua)
		if err != nil {
			return out, err
		}
		var data map[string]downloadURLData
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			return out, fmt.Errorf("115: 解析下载地址失败：%w", err)
		}
		// 响应以文件 ID 为键，条目内的 pick_code 用于映射回请求侧
		for _, item := range data {
			if item.PickCode == "" || item.URL.URL == "" {
				continue
			}
			SetDownloadURLCache(item.PickCode, item.URL.URL, ua)
			out[item.PickCode] = item.URL.URL
		}
	}
	return out, nil
}

// ─── 授权（设备码扫码） ──────────────────────────────────────────────────────

// QrCodeScanStatus 扫码状态。
type QrCodeScanStatus int

const (
	QrCodeScanStatusExpired    QrCodeScanStatus = 5
	QrCodeScanStatusNotScanned QrCodeScanStatus = 2
	QrCodeScanStatusScanned    QrCodeScanStatus = 3
	QrCodeScanStatusConfirmed  QrCodeScanStatus = 4
)

func (s QrCodeScanStatus) String() string {
	switch s {
	case QrCodeScanStatusNotScanned:
		return "waiting"
	case QrCodeScanStatusScanned:
		return "scanned"
	case QrCodeScanStatusConfirmed:
		return "confirmed"
	default:
		return "expired"
	}
}

func (s QrCodeScanStatus) Tip() string {
	switch s {
	case QrCodeScanStatusNotScanned:
		return "等待扫码"
	case QrCodeScanStatusScanned:
		return "已扫码，请在 115 客户端确认"
	case QrCodeScanStatusConfirmed:
		return "授权成功"
	default:
		return "二维码已过期"
	}
}

// QrCodeData 是设备码二维码数据。
type QrCodeData struct {
	Uid    string `json:"uid"`
	Time   int64  `json:"time"`
	Sign   string `json:"sign"`
	Qrcode string `json:"qrcode"` // 二维码图片 URL
}

// QrCodeDataReturn 含 PKCE code_verifier。
type QrCodeDataReturn struct {
	QrCodeData
	CodeVerifier string `json:"code_verifier"`
}

// TokenData 是 115 访问令牌。
type TokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// GetQrCode 获取设备码登录二维码。
func (c *OpenClient) GetQrCode() (*QrCodeDataReturn, error) {
	codeVerifier := RandomString(64)
	params := map[string]string{
		"client_id":             c.AppID,
		"code_challenge":        genCodeChallenge(codeVerifier),
		"code_challenge_method": "sha256",
	}
	resp, err := c.doJSON(context.Background(), "POST", PassportAPIBase+"/open/authDeviceCode", params, false, 1)
	if err != nil {
		return nil, err
	}
	code, err := openFirstList[QrCodeData](resp.Data)
	if err != nil {
		return nil, err
	}
	// 关键字段缺失时显式报错：空 uid/sign 会导致后续扫码轮询必然失败，
	// 不能把残缺响应当成功返回给界面。
	if code.Uid == "" || code.Sign == "" {
		return nil, fmt.Errorf("115: 设备码响应缺少 uid/sign，无法发起扫码授权")
	}
	return &QrCodeDataReturn{QrCodeData: *code, CodeVerifier: codeVerifier}, nil
}

// QrCodeScanStatus 查询扫码状态。
func (c *OpenClient) QrCodeScanStatus(codeData *QrCodeData) (QrCodeScanStatus, error) {
	if codeData == nil {
		return QrCodeScanStatusExpired, fmt.Errorf("空二维码数据")
	}
	params := map[string]string{
		"uid":  codeData.Uid,
		"time": fmt.Sprint(codeData.Time),
		"sign": codeData.Sign,
	}
	resp, err := c.doJSON(context.Background(), "GET", QRCodeAPIBase+"/get/status/", params, false, 1)
	if err != nil {
		return QrCodeScanStatusExpired, err
	}
	status, err := openFirstList[struct {
		Status int `json:"status"` // 0 未扫码 1 已扫码 2 已确认
	}](resp.Data)
	if err != nil {
		return QrCodeScanStatusExpired, err
	}
	switch status.Status {
	case 1:
		return QrCodeScanStatusScanned, nil
	case 2:
		return QrCodeScanStatusConfirmed, nil
	case 0:
		return QrCodeScanStatusNotScanned, nil
	default:
		return QrCodeScanStatusExpired, nil
	}
}

// GetToken 用设备码换访问令牌。
func (c *OpenClient) GetToken(qrCode *QrCodeDataReturn) (*TokenData, error) {
	if qrCode == nil || qrCode.Uid == "" {
		return nil, fmt.Errorf("空二维码数据")
	}
	params := map[string]string{
		"uid":           qrCode.Uid,
		"code_verifier": qrCode.CodeVerifier,
	}
	resp, err := c.doJSON(context.Background(), "POST", PassportAPIBase+"/open/deviceCodeToToken", params, false, 0)
	if err != nil {
		return nil, err
	}
	token, err := openFirstList[TokenData](resp.Data)
	if err != nil {
		return nil, err
	}
	// 空凭证绝不能 SetAuthToken 后当成功返回：界面会显示"授权成功"但账号不可用
	if token.AccessToken == "" || token.RefreshToken == "" {
		return nil, fmt.Errorf("115: 设备码换 token 返回空凭证（access_token/refresh_token 缺失）")
	}
	c.SetAuthToken(token.AccessToken, token.RefreshToken)
	return token, nil
}

// RefreshToken 刷新访问令牌。
func (c *OpenClient) RefreshToken(refreshToken string) (*TokenData, error) {
	c.tokenMu.Lock()
	if refreshToken == "" {
		refreshToken = c.RefreshTokenStr
	}
	if refreshToken == "" {
		c.tokenMu.Unlock()
		return nil, fmt.Errorf("没有可用的 refresh_token")
	}
	token, err := c.doRefreshToken(refreshToken)
	if err != nil {
		// refresh_token 已失效时清空内存令牌（提示需重新授权）
		if IsRefreshTokenDead(err) {
			c.setAuthTokenLocked("", "")
		}
		c.tokenMu.Unlock()
		return nil, err
	}
	if token.AccessToken == "" || token.RefreshToken == "" {
		c.tokenMu.Unlock()
		return nil, fmt.Errorf("115: 刷新返回空凭证（access_token/refresh_token 缺失）")
	}
	c.setAuthTokenLocked(token.AccessToken, token.RefreshToken)
	c.tokenMu.Unlock()
	if c.OnTokenRefreshed != nil {
		c.OnTokenRefreshed(token.AccessToken, token.RefreshToken)
	}
	return token, nil
}

// doRefreshToken 调用 115 刷新接口换取新令牌，不修改客户端内存状态；
// 拆出无状态方法供 tryRefreshTokenLocked（已持 tokenMu 写锁）复用，
// 避免在持锁期间重入 SetAuthToken 造成死锁。
func (c *OpenClient) doRefreshToken(refreshToken string) (*TokenData, error) {
	params := map[string]string{"refresh_token": refreshToken}
	resp, err := c.doJSON(context.Background(), "POST", PassportAPIBase+"/open/refreshToken", params, false, 0)
	if err != nil && resp == nil {
		return nil, err
	}
	if resp == nil {
		return nil, err
	}
	if !resp.State {
		return nil, NewOpenAPIResponseError(resp.Code, resp.Errno, resp.Message, resp.Error, "115 开放平台刷新访问凭证失败")
	}
	return openFirstList[TokenData](resp.Data)
}

// ─── 用户信息 ──────────────────────────────────────────────────────────────────

// UserInfo 是 115 用户信息。
type UserInfo struct {
	UserId   json.Number `json:"user_id"`
	UserName string      `json:"user_name"`
}

// FetchUserInfo 获取用户信息。
func (c *OpenClient) FetchUserInfo(ctx context.Context) (*UserInfo, error) {
	resp, err := c.doAuthJSON(ctx, "GET", ProAPIBase+"/open/user/info", nil, 1)
	if err != nil {
		return nil, err
	}
	var info UserInfo
	if err := json.Unmarshal(resp.Data, &info); err != nil {
		return nil, fmt.Errorf("115: 解析用户信息失败：%w", err)
	}
	return &info, nil
}
