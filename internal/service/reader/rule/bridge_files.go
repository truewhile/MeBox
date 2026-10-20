package rule

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/dop251/goja"
)

// 本文件实现 legado JsExtensions 里的「本地文件」能力
// （cacheFile / downloadFile / readFile / readTxtFile / deleteFile / getFile）。
//
// 与 legado 的语义保持一致：
//   - downloadFile(url) 下载到缓存目录，返回「相对缓存根目录」的路径；
//   - cacheFile(url) 下载（带缓存）后返回文件的**文本内容**（不是路径）；
//   - readFile/readTxtFile/deleteFile/getFile 接受相对路径（允许以 / 开头），
//     一律解析到缓存目录下。
//
// 服务端把缓存根目录固定为 <cache>/reader/files，并且只允许访问该目录内的文件，
// 避免书源通过 `../../` 之类读到宿主上的任意文件。

// filesSubdir 书源文件缓存目录（相对 ReaderService 的缓存根目录）。
const filesSubdir = "reader/files"

// readerFilesDir 返回书源文件缓存目录；未配置缓存目录时返回空串。
func readerFilesDir(cacheDir string) string {
	if strings.TrimSpace(cacheDir) == "" {
		return ""
	}
	return filepath.Join(cacheDir, "reader", "files")
}

// cacheFileName 计算某个 URL 在缓存目录里的文件名（对应 legado 的
// `${md5Encode16(url)}.${type}`：md5 十六进制取中间 16 位，保留 URL 后缀）。
func cacheFileName(rawURL string) string {
	name := md5Hex(rawURL, true)
	if ext := urlFileSuffix(rawURL); ext != "" {
		name += "." + ext
	}
	return name
}

// urlFileSuffix 取 URL 路径部分的后缀（不含 `.`），只接受字母数字（长度 1~8）。
func urlFileSuffix(rawURL string) string {
	u := rawURL
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	if i := strings.LastIndex(u, "/"); i >= 0 {
		u = u[i+1:]
	}
	dot := strings.LastIndex(u, ".")
	if dot < 0 || dot == len(u)-1 {
		return ""
	}
	ext := u[dot+1:]
	if len(ext) > 8 {
		return ""
	}
	for i := 0; i < len(ext); i++ {
		c := ext[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return ""
		}
	}
	return strings.ToLower(ext)
}

// resolveCacheFilePath 把书源给的相对路径解析成缓存目录下的绝对路径。
// 允许以 `/` 开头（downloadFile 的返回值就是这种形态），但拒绝越出缓存目录。
func resolveCacheFilePath(cacheDir, p string) (string, error) {
	dir := readerFilesDir(cacheDir)
	if dir == "" {
		return "", fmt.Errorf("未配置缓存目录")
	}
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("文件路径为空")
	}
	rel := filepath.Clean(filepath.FromSlash(strings.Trim(p, `/\`)))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("非法文件路径: %s", p)
	}
	return filepath.Join(dir, rel), nil
}

// writeCacheFile 把字节写入缓存目录，返回相对路径（以 / 开头，对应 legado）。
func writeCacheFile(cacheDir, name string, data []byte) (string, error) {
	dir := readerFilesDir(cacheDir)
	if dir == "" {
		return "", fmt.Errorf("未配置缓存目录")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		return "", err
	}
	return "/" + name, nil
}

// downloadToCache 下载 URL 并落到缓存目录（已存在则直接复用），
// 返回相对缓存根目录的路径。下载走书源会话，因此会带上书源请求头与 Cookie。
func downloadToCache(r *JSRunner, rawURL string) (string, error) {
	dir := readerFilesDir(r.cfg.CacheDir)
	if dir == "" {
		return "", fmt.Errorf("未配置缓存目录")
	}
	name := cacheFileName(rawURL)
	full := filepath.Join(dir, name)
	if st, err := os.Stat(full); err == nil && st.Size() > 0 {
		return "/" + name, nil
	}
	req, err := ParseAnalyzeUrlWithJS(rawURL, "", 0, r.cfg.BaseURL, r)
	if err != nil {
		return "", err
	}
	if req.Unsupported != nil {
		return "", req.Unsupported
	}
	// Raw：响应按原始字节返回，避免 charset 解码破坏二进制/非 UTF-8 文本。
	// 同时清掉 HexBody：URL 选项里的 `type` 在 legado 是文件后缀提示，
	// 这里不该把下载内容按 hex 字符串落盘。
	req.Raw = true
	req.HexBody = false
	body, _, code, err := r.fetch(req)
	if err != nil {
		return "", err
	}
	if code >= 400 {
		return "", fmt.Errorf("下载失败 HTTP %d: %s", code, rawURL)
	}
	return writeCacheFile(r.cfg.CacheDir, name, []byte(body))
}

// decodeTextAuto 解码文本文件：UTF-8 BOM → 去 BOM；合法 UTF-8 → 原样；
// 否则按 GBK 解（中文站点/书源最常见的另一种编码），失败再原样返回。
func decodeTextAuto(b []byte) string {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return string(b[3:])
	}
	if utf8.Valid(b) {
		return string(b)
	}
	if s, err := DecodeBytes(b, "gbk"); err == nil && s != "" {
		return s
	}
	return string(b)
}

// fileSuffixFromName 取路径的最后一段（getFile 的 name 语义）。
func fileSuffixFromName(p string) string {
	return path.Base(strings.ReplaceAll(p, "\\", "/"))
}

// newFileObject 构造 `java.getFile(path)` 返回的 File 等价物，
// 提供书源常用的 exists / length / name / delete / readText。
func newFileObject(vm *goja.Runtime, full, raw string) *goja.Object {
	o := vm.NewObject()
	set := func(k string, v any) { _ = o.Set(k, v) }
	set("path", full)
	set("name", fileSuffixFromName(raw))
	set("exists", func(goja.FunctionCall) goja.Value {
		if full == "" {
			return vm.ToValue(false)
		}
		_, err := os.Stat(full)
		return vm.ToValue(err == nil)
	})
	set("isDirectory", func(goja.FunctionCall) goja.Value {
		if full == "" {
			return vm.ToValue(false)
		}
		st, err := os.Stat(full)
		return vm.ToValue(err == nil && st.IsDir())
	})
	set("length", func(goja.FunctionCall) goja.Value {
		if full == "" {
			return vm.ToValue(0)
		}
		st, err := os.Stat(full)
		if err != nil {
			return vm.ToValue(0)
		}
		return vm.ToValue(st.Size())
	})
	set("delete", func(goja.FunctionCall) goja.Value {
		if full == "" {
			return vm.ToValue(false)
		}
		return vm.ToValue(os.Remove(full) == nil)
	})
	set("readText", func(goja.FunctionCall) goja.Value {
		if full == "" {
			return vm.ToValue("")
		}
		b, err := os.ReadFile(full)
		if err != nil {
			return vm.ToValue("")
		}
		return vm.ToValue(decodeTextAuto(b))
	})
	set("toString", func(goja.FunctionCall) goja.Value { return vm.ToValue(full) })
	return o
}
