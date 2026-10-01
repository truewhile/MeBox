// 本地书籍导入与阅读（对应 legado 的本地 TXT / EPUB 书）。
//
// 存储：正文统一落到 data/reader/local/<bookID>.<txt|epub>，目录信息与网络书
// 一样存 reader_chapters，用 ReaderChapter.Tag 记住定位信息：
//   - TXT：UTF-8 规范化后文件内的字节区间 "start:end"（读章节只读这段，不载整本）
//   - EPUB：zip 内的 XHTML 条目路径（每次按需解压该条）
// 网络书籍不受影响：ReaderBook.LocalPath 为空即走书源链路。
package reader

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service/reader/rule"
)

// LocalBookMaxBytes 单个本地书籍文件大小上限。
const LocalBookMaxBytes = 64 << 20

// localBooksDirName data 目录下的存放位置。
const localBooksDirName = "local"

// txtChapterPattern TXT 目录正则（对齐 legado 默认 TXT 目录规则：RE2 不支持
// lookbehind，改成行首锚定）：
//
//	第 1 章 / 第1章 / 第十二节 / 序章 / 楔子 / 番外 ...
//
// 分成「章标记」+「章名」两段捕获，后面再看章名像不像句子。
var txtChapterPattern = regexp.MustCompile(
	`(?m)^[ \t　]{0,4}(序章|楔子|正文|终章|后记|尾声|番外|第[ \t]{0,4}[0-9〇零一二两三四五六七八九十百千万壹贰叁肆伍陆柒捌玖拾佰仟]{1,12}[ \t]{0,4}[章节卷集部篇])([^\n]{0,40})$`)

// txtSentenceTail 章名段命中这些，基本可断定这行是正文而不是章名
// （例如「第一章的正文内容。」）。
var txtSentenceTail = regexp.MustCompile(`^[的的是了在有和与就都也还很最]|[。！？；：]`)

// looksLikeChapterTitle 判断匹配到的一行是不是真的章名。
func looksLikeChapterTitle(marker, rest string) bool {
	if marker == "" {
		return false
	}
	if txtSentenceTail.MatchString(strings.TrimSpace(rest)) {
		return false
	}
	// 章名一般不短于标记本身太多，留够「第1章 开端（上）」这类
	return len([]rune(marker+rest)) <= 40
}

// txtChapter 章节在规范化后 UTF-8 文本里的字节区间（End 不含）。
type txtChapter struct {
	Title      string
	Start, End int
}

// epubChapter EPUB 里的一章：zip 条目路径 + 标题。
type epubChapter struct {
	Title string
	Href  string
}

// localBooksDir 本地书籍存放目录（不存在则创建）。
func (s *ReaderService) localBooksDir() (string, error) {
	dir := filepath.Join(s.cfg.App.DataDir, "reader", localBooksDirName)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("创建本地书籍目录失败: %w", err)
	}
	return dir, nil
}

// localFilePath 某本本地书籍的落盘路径。
//
// LocalExternal 为真时 LocalPath 是服务器上的绝对路径（原地引用，导入时由
// 管理员选定）；否则是 data/reader/local 下的托管副本文件名。
func (s *ReaderService) localFilePath(book *model.ReaderBook) (string, error) {
	if book.LocalPath == "" {
		return "", fmt.Errorf("不是本地书籍")
	}
	if book.LocalExternal {
		return filepath.Clean(book.LocalPath), nil
	}
	dir, err := s.localBooksDir()
	if err != nil {
		return "", err
	}
	// 托管副本的文件名只由服务端生成（<id>.<ext>），这里再挡一次路径穿越
	return filepath.Join(dir, filepath.Base(book.LocalPath)), nil
}

// localBookPayload 本地书籍的解析结果（上传与「从服务器路径导入」共用）。
type localBookPayload struct {
	title      string
	ext        string
	charset    string
	chaps      []model.ReaderChapter
	coverEntry string
	// payload 原始字节，仅托管副本模式需要落盘
	payload []byte
}

// parseLocalBookPayload 按内容识别 TXT / EPUB 并切好目录。
func parseLocalBookPayload(name string, data []byte) (*localBookPayload, error) {
	isEPUB := bytes.HasPrefix(data, []byte("PK\x03\x04"))
	if !isEPUB && !strings.EqualFold(filepath.Ext(name), ".epub") && !strings.EqualFold(filepath.Ext(name), ".txt") {
		return nil, fmt.Errorf("仅支持 TXT / EPUB 文件")
	}
	out := &localBookPayload{title: strings.TrimSuffix(name, filepath.Ext(name))}
	if isEPUB {
		list, cover, err := parseEPUB(data)
		if err != nil {
			return nil, err
		}
		if len(list) == 0 {
			return nil, fmt.Errorf("EPUB 里没有可读章节")
		}
		out.ext, out.coverEntry = ".epub", cover
		out.chaps = make([]model.ReaderChapter, 0, len(list))
		for i, c := range list {
			out.chaps = append(out.chaps, model.ReaderChapter{Index: i, Title: c.Title, Tag: c.Href})
		}
		return out, nil
	}
	decoded, charset := decodeTextFile(data)
	if strings.TrimSpace(decoded) == "" {
		return nil, fmt.Errorf("文件内容为空或无法解码")
	}
	out.ext, out.charset = ".txt", charset
	for i, c := range splitTXTChapters(decoded) {
		out.chaps = append(out.chaps, model.ReaderChapter{
			Index: i, Title: c.Title, Tag: fmt.Sprintf("%d:%d", c.Start, c.End),
		})
	}
	return out, nil
}

// localImportSpec 本地书籍落库参数。payload 非空走托管副本（写入
// data/reader/local）；externalPath 非空走原地引用（LocalPath 直接存它）。
type localImportSpec struct {
	key          string // FindBookByURL 的去重键（存进 BookURL）
	title        string
	ext          string
	charset      string
	bookType     int
	chaps        []model.ReaderChapter
	payload      []byte
	externalPath string
	coverEntry   string
}

// importLocalBook 本地书籍落库共同路径：去重 → 建/更新 → 落内容 → 写目录。
//
// 重复导入按“覆盖更新”处理：章节数没变就保留阅读进度，变了则退回第一章。
func (s *ReaderService) importLocalBook(
	ctx context.Context, userID string, spec localImportSpec,
) (*model.ReaderBook, error) {
	if len(spec.chaps) == 0 {
		return nil, fmt.Errorf("未解析出任何章节")
	}
	book, err := s.repo.FindBookByURL(ctx, userID, "", spec.key)
	if err != nil || book == nil {
		book = &model.ReaderBook{}
	}
	isNew := book.ID == ""
	if isNew {
		book.UserID = userID
		book.OriginName = "本地导入"
		book.Type = spec.bookType
	}
	book.Name = spec.title
	book.BookURL = spec.key
	book.Charset = spec.charset
	book.TotalChapterNum = len(spec.chaps)
	book.IsLocal = true

	managed := spec.externalPath == ""
	if managed {
		book.LocalExternal = false
		book.LocalPath = "placeholder" + spec.ext // 真正的文件名在写入后按 ID 生成
	} else {
		if len(spec.externalPath) > 255 {
			return nil, fmt.Errorf("路径过长，无法保存引用")
		}
		book.LocalExternal = true
		book.LocalPath = filepath.Clean(spec.externalPath)
	}

	if isNew {
		if err := s.repo.CreateBook(ctx, book); err != nil {
			return nil, fmt.Errorf("创建本地书籍失败: %w", err)
		}
	}
	if managed {
		// 落盘（文件名用 ID，避免同名文件互相覆盖）
		book.LocalPath = book.ID + spec.ext
		path, err := s.localFilePath(book)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, spec.payload, 0o640); err != nil {
			return nil, fmt.Errorf("保存书籍文件失败: %w", err)
		}
	}
	// EPUB 封面：从 zip 里解析出的封面图转成签名资源地址，供书架/阅读页展示。
	// 覆盖导入时按新文件重置，避免留下指向已不存在条目的旧封面。
	if spec.ext == ".epub" {
		book.CoverURL = ""
		if spec.coverEntry != "" {
			book.CoverURL = s.LocalAssetURL(book.ID, spec.coverEntry)
		}
	}

	if !isNew {
		// 覆盖更新：章节数不变就保留进度，否则退回第一章
		if old, err := s.repo.ListChapters(ctx, book.ID); err == nil && len(old) != len(spec.chaps) {
			book.DurChapterIndex = 0
			book.DurChapterPos = 0
			book.DurChapterTitle = ""
		}
	}
	chaps := spec.chaps
	for i := range chaps {
		chaps[i].BookID = book.ID
	}
	if err := s.repo.ReplaceChapters(ctx, book.ID, chaps); err != nil {
		return nil, fmt.Errorf("写入目录失败: %w", err)
	}
	if err := s.repo.UpdateBook(ctx, book); err != nil {
		return nil, fmt.Errorf("更新书籍失败: %w", err)
	}
	book.IsLocal = true
	return book, nil
}

// ImportLocalBook 导入一本本地书籍（TXT / EPUB），内容复制进 data/reader/local。
//
// 同名（同 user + 文件名）重复导入按“覆盖更新”处理。
func (s *ReaderService) ImportLocalBook(
	ctx context.Context, userID, filename string, data []byte,
) (*model.ReaderBook, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("上传内容为空")
	}
	if len(data) > LocalBookMaxBytes {
		return nil, fmt.Errorf("文件超过 %dMB", LocalBookMaxBytes>>20)
	}
	name := strings.TrimSpace(filepath.Base(strings.ReplaceAll(filename, "\\", "/")))
	if name == "" || name == "." {
		return nil, fmt.Errorf("缺少文件名")
	}
	parsed, err := parseLocalBookPayload(name, data)
	if err != nil {
		return nil, err
	}
	return s.importLocalBook(ctx, userID, localImportSpec{
		key:        name,
		title:      parsed.title,
		ext:        parsed.ext,
		charset:    parsed.charset,
		chaps:      parsed.chaps,
		payload:    data,
		coverEntry: parsed.coverEntry,
	})
}

// ImportLocalBookFromPath 从服务器上已有的文件导入书籍（TXT / EPUB）。
//
// 原地引用，不复制到 data/reader/local；同一个文件重复导入按覆盖更新处理，
// 以绝对路径作为去重键（不同目录下的同名文件互不干扰）。
func (s *ReaderService) ImportLocalBookFromPath(
	ctx context.Context, userID, path string,
) (*model.ReaderBook, error) {
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return nil, fmt.Errorf("路径无效: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("文件不存在或不可读")
	}
	if info.IsDir() {
		return nil, fmt.Errorf("请选择文件而不是目录")
	}
	if info.Size() > LocalBookMaxBytes {
		return nil, fmt.Errorf("文件超过 %dMB", LocalBookMaxBytes>>20)
	}
	f, err := os.Open(abs) // #nosec G304 -- 路径由管理员通过文件选择器选定，handler 已校验允许根目录
	if err != nil {
		return nil, fmt.Errorf("读取文件失败: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, LocalBookMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取文件失败: %w", err)
	}
	name := filepath.Base(abs)
	parsed, err := parseLocalBookPayload(name, data)
	if err != nil {
		return nil, err
	}
	return s.importLocalBook(ctx, userID, localImportSpec{
		key:          abs,
		title:        parsed.title,
		ext:          parsed.ext,
		charset:      parsed.charset,
		chaps:        parsed.chaps,
		externalPath: abs,
		coverEntry:   parsed.coverEntry,
	})
}

// audioExtensions 有声书识别的音频后缀；.strm 是播放指针，单独处理。
var audioExtensions = map[string]bool{
	".mp3": true, ".m4a": true, ".m4b": true, ".aac": true, ".flac": true,
	".wav": true, ".ogg": true, ".oga": true, ".opus": true, ".wma": true, ".ape": true,
}

// ImportLocalAudioDir 把一个服务器目录导入为一本有声书。
//
// 目录（含子目录）下的音频文件与 .strm 指针按相对路径排序后逐个成为章节，
// 原地引用不复制；重复导入同一个目录按覆盖更新处理。
func (s *ReaderService) ImportLocalAudioDir(
	ctx context.Context, userID, dir string,
) (*model.ReaderBook, error) {
	abs, err := filepath.Abs(strings.TrimSpace(dir))
	if err != nil {
		return nil, fmt.Errorf("路径无效: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("目录不存在或不可读")
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("请选择目录")
	}

	type audioEntry struct {
		path string
		rel  string
	}
	var tracks []audioEntry
	walkErr := filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 单个条目不可读不影响整体
		}
		if strings.HasPrefix(d.Name(), ".") && p != abs {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if !audioExtensions[ext] && ext != ".strm" {
			return nil
		}
		rel, relErr := filepath.Rel(abs, p)
		if relErr != nil {
			rel = d.Name()
		}
		tracks = append(tracks, audioEntry{path: p, rel: rel})
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("扫描目录失败: %w", walkErr)
	}
	if len(tracks) == 0 {
		return nil, fmt.Errorf("目录里没有找到音频文件（支持 .strm 播放指针）")
	}
	sort.Slice(tracks, func(i, j int) bool { return tracks[i].rel < tracks[j].rel })

	chaps := make([]model.ReaderChapter, 0, len(tracks))
	for i, t := range tracks {
		chaps = append(chaps, model.ReaderChapter{
			Index: i,
			Title: strings.TrimSuffix(filepath.Base(t.rel), filepath.Ext(t.rel)),
			Tag:   t.path,
		})
	}
	return s.importLocalBook(ctx, userID, localImportSpec{
		key:          abs,
		title:        filepath.Base(abs),
		bookType:     1, // 音频
		chaps:        chaps,
		externalPath: abs,
	})
}

// OpenLocalAudio 打开本地音频文件供流式下发（支持 Range，可拖动进度）。
func (s *ReaderService) OpenLocalAudio(path string) (*os.File, os.FileInfo, error) {
	f, err := os.Open(filepath.Clean(path)) // #nosec G304 -- 路径由签名校验通过
	if err != nil {
		return nil, nil, fmt.Errorf("音频文件已丢失")
	}
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		f.Close()
		return nil, nil, fmt.Errorf("音频文件不可用")
	}
	return f, info, nil
}

// BackfillLocalCover 给历史导入的本地 EPUB 补上封面地址。
//
// 封面是导入时解析出来落库的，早期版本没这一步；在书架加载时自愈一次，
// 用户就不必为了拿到封面而重新导入。返回是否写回了数据库。
func (s *ReaderService) BackfillLocalCover(ctx context.Context, book *model.ReaderBook) bool {
	if book == nil || book.CoverURL != "" || book.LocalPath == "" {
		return false
	}
	if !strings.EqualFold(filepath.Ext(book.LocalPath), ".epub") {
		return false
	}
	path, err := s.localFilePath(book)
	if err != nil {
		return false
	}
	f, err := os.Open(path) // #nosec G304 -- path 由服务端按书籍 ID 生成
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false
	}
	zr, err := zip.NewReader(f, st.Size())
	if err != nil {
		return false
	}
	entry := newEPUBZipReader(zr.File).coverEntry()
	if entry == "" {
		return false
	}
	book.CoverURL = s.LocalAssetURL(book.ID, entry)
	if err := s.repo.UpdateBook(ctx, book); err != nil {
		if s.log != nil {
			s.log.Warn("reader: 回填本地书籍封面失败",
				zap.String("book", book.ID), zap.Error(err))
		}
		return false
	}
	return true
}

// LocalChapterContent 读本地书籍的某一章正文（不做书源抓取）。
func (s *ReaderService) LocalChapterContent(
	ctx context.Context, userID, bookID string, chapterIndex int,
) (*ChapterContent, error) {
	book, err := s.repo.GetBook(ctx, bookID)
	if err != nil {
		return nil, err
	}
	if book.UserID != userID {
		return nil, fmt.Errorf("无权操作他人书架")
	}
	if book.LocalPath == "" {
		return nil, fmt.Errorf("不是本地书籍")
	}
	chapters, err := s.repo.ListChapters(ctx, bookID)
	if err != nil {
		return nil, err
	}
	if chapterIndex < 0 || chapterIndex >= len(chapters) {
		return nil, fmt.Errorf("章节序号越界（共 %d 章）", len(chapters))
	}
	ch := chapters[chapterIndex]
	// 本地有声书：一章 = 一个音频文件或一个 .strm 播放指针
	if book.Type == 1 {
		track, transcoding, err := s.localAudioTrack(book, ch)
		if err != nil {
			return nil, err
		}
		return &ChapterContent{
			Type: "audio", Tracks: []string{track}, Transcoding: transcoding, declaredType: -1,
		}, nil
	}
	text, err := s.readLocalChapter(book, ch)
	if err != nil {
		return nil, err
	}
	// EPUB 里的图片换成签名地址（前端按 [img] 标记渲染）
	text = s.rewriteLocalImages(book.ID, text)
	text = s.applyUserReplaceRules(ctx, userID, book.Name, text)
	return &ChapterContent{Type: "text", Content: text, declaredType: -1}, nil
}

// localAudioTrack 把本地有声书的一章解成可播放的签名地址：
// .strm 指针解析成远端地址（走媒体代理）或本地文件（走本地音频端点）；
// 浏览器解不了的格式（WMA 等）改走转码端点。第二个返回值表示该章需要转码。
func (s *ReaderService) localAudioTrack(book *model.ReaderBook, ch model.ReaderChapter) (string, bool, error) {
	target := strings.TrimSpace(ch.Tag)
	if target == "" {
		return "", false, fmt.Errorf("章节定位信息损坏，请重新导入该书")
	}
	if strings.EqualFold(filepath.Ext(target), ".strm") {
		raw, err := readSTRMFile(target)
		if err != nil {
			return "", false, fmt.Errorf("读取 .strm 失败: %w", err)
		}
		if raw == "" {
			return "", false, fmt.Errorf("该 .strm 里没有可用的播放地址")
		}
		// 本地路径：相对 .strm 所在目录解析
		target = raw
		if !isRemoteMediaURL(target) && !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(ch.Tag), target)
		}
	}
	if needsAudioTranscode(target) {
		if s.ffmpegBinary() == "" {
			return "", false, transcodeMissingFFmpegError(target)
		}
		return s.AudioTranscodeURL(book.ID, target), true, nil
	}
	if isRemoteMediaURL(target) {
		return s.ProxyURL(book.ID, target), false, nil
	}
	return s.LocalAudioURL(book.ID, filepath.Clean(target)), false, nil
}

// readSTRMFile 读取 .strm 指针文件里的第一条有效内容（约定为一行播放地址）。
func readSTRMFile(path string) (string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- 路径来自管理员导入时选定的目录
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		candidate := strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if candidate == "" || strings.HasPrefix(candidate, "#") {
			continue
		}
		return candidate, nil
	}
	return "", nil
}

// isRemoteMediaURL 判断 .strm 内容是不是远端地址（http/网盘协议或本机播放端点）；
// 不是的话按同目录下的本地文件路径处理。
func isRemoteMediaURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "/api/") || strings.HasPrefix(raw, "/Videos/") || strings.HasPrefix(raw, "/videos/") {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "webdav", "davs", "alist", "alists", "openlist", "openlists":
		return true
	}
	return false
}

// readLocalChapter 取一章正文：TXT 读字节区间，EPUB 解压对应条目再转纯文本。
func (s *ReaderService) readLocalChapter(book *model.ReaderBook, ch model.ReaderChapter) (string, error) {
	path, err := s.localFilePath(book)
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(book.LocalPath, ".epub") {
		return readEPUBEntry(path, ch.Tag)
	}
	start, end, ok := parseByteRange(ch.Tag)
	if !ok {
		return "", fmt.Errorf("章节定位信息损坏，请重新导入该书")
	}
	f, err := os.Open(path) // #nosec G304 -- path 由服务端按书籍 ID 生成
	if err != nil {
		return "", fmt.Errorf("本地书籍文件已丢失: %w", err)
	}
	defer f.Close()
	if end <= start {
		return "", nil
	}
	buf := make([]byte, end-start)
	if _, err := f.ReadAt(buf, int64(start)); err != nil && err != io.EOF {
		return "", fmt.Errorf("读取章节失败: %w", err)
	}
	return strings.TrimSpace(string(buf)), nil
}

// DeleteLocalBookFile 移出书架时删除本地文件（网络书籍无文件，直接返回）。
//
// 原地引用的书指向服务器上已有的文件/目录，移出书架只解除引用，不动源文件。
func (s *ReaderService) DeleteLocalBookFile(book *model.ReaderBook) {
	if book == nil || book.LocalPath == "" || book.LocalExternal {
		return
	}
	path, err := s.localFilePath(book)
	if err != nil {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) && s.log != nil {
		s.log.Warn("reader: 删除本地书籍文件失败",
			zap.String("book", book.ID), zap.Error(err))
	}
}

// parseByteRange 解析 "start:end"。
func parseByteRange(tag string) (int, int, bool) {
	parts := strings.SplitN(tag, ":", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	var start, end int
	if _, err := fmt.Sscanf(parts[0], "%d", &start); err != nil {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &end); err != nil {
		return 0, 0, false
	}
	if start < 0 || end < start {
		return 0, 0, false
	}
	return start, end, true
}

// ─── TXT ──────────────────────────────────────────────────────────────────

// decodeTextFile 识别编码并统一转成 UTF-8 字符串，返回检测到的字符集名。
func decodeTextFile(data []byte) (string, string) {
	// UTF-8 BOM
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return string(data[3:]), "utf-8"
	}
	// UTF-16 BOM
	if len(data) >= 2 {
		if data[0] == 0xFF && data[1] == 0xFE {
			if s, err := rule.DecodeBytes(data, "utf-16le"); err == nil {
				return strings.TrimPrefix(s, "\uFEFF"), "utf-16le"
			}
		}
		if data[0] == 0xFE && data[1] == 0xFF {
			if s, err := rule.DecodeBytes(data, "utf-16be"); err == nil {
				return strings.TrimPrefix(s, "\uFEFF"), "utf-16be"
			}
		}
	}
	if utf8.Valid(data) {
		return string(data), "utf-8"
	}
	// 中文小说最常见的另一种编码
	if s, err := rule.DecodeBytes(data, "gbk"); err == nil && utf8.ValidString(s) {
		return s, "gbk"
	}
	if s, err := rule.DecodeBytes(data, "big5"); err == nil && utf8.ValidString(s) {
		return s, "big5"
	}
	return string(data), "utf-8"
}

// splitTXTChapters 按目录正则切章；切不出来就整本当一章。
// 用字节下标而不是 rune 下标，方便直接按区间读文件。
func splitTXTChapters(text string) []txtChapter {
	matches := txtChapterPattern.FindAllStringSubmatchIndex(text, -1)
	var starts []int
	var titles []string
	for _, m := range matches {
		// m: [整行起, 整行止, 标记起, 标记止, 章名起, 章名止]
		marker := text[m[2]:m[3]]
		rest := text[m[4]:m[5]]
		if !looksLikeChapterTitle(marker, rest) {
			continue
		}
		starts = append(starts, m[0])
		titles = append(titles, strings.TrimSpace(text[m[0]:m[1]]))
	}
	if len(starts) == 0 {
		return []txtChapter{{Title: "全文", Start: 0, End: len(text)}}
	}
	out := make([]txtChapter, 0, len(starts))
	for i, start := range starts {
		end := len(text)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		// 章前内容（书名、作者、简介）跟着第一章
		if i == 0 {
			start = 0
		}
		out = append(out, txtChapter{Title: titles[i], Start: start, End: end})
	}
	return out
}

// ─── EPUB ─────────────────────────────────────────────────────────────────

// parseEPUB 读 EPUB 的 OPF spine，得到有序章节；标题优先取 NCX/NAV 目录，其次取
// 正文里的首个标题标签，最后退回“第 N 章”。同时返回封面图在 zip 里的条目路径
// （取不到返回空串）。
func parseEPUB(data []byte) ([]epubChapter, string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, "", fmt.Errorf("EPUB 损坏（不是有效的 zip）: %w", err)
	}
	return parseEPUBReader(newEPUBZipReader(zr.File))
}

// epubZipReader 已打开 zip 的条目索引，章节解析与封面回填共用。
type epubZipReader struct {
	entries map[string]*zip.File
}

func newEPUBZipReader(files []*zip.File) *epubZipReader {
	entries := make(map[string]*zip.File, len(files))
	for _, f := range files {
		entries[filepath.Clean(f.Name)] = f
	}
	return &epubZipReader{entries: entries}
}

func (r *epubZipReader) read(name string) ([]byte, error) {
	f, ok := r.entries[filepath.Clean(name)]
	if !ok {
		return nil, fmt.Errorf("EPUB 缺少条目 %s", name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, LocalBookMaxBytes))
}

// coverEntry 解析 OPF 里的封面声明，返回封面图在 zip 里的条目路径（取不到为空）。
func (r *epubZipReader) coverEntry() string {
	opfPath, err := epubOPFPath(r.read)
	if err != nil {
		return ""
	}
	opfRaw, err := r.read(opfPath)
	if err != nil {
		return ""
	}
	opf, err := parseOPF(opfRaw)
	if err != nil {
		return ""
	}
	return epubCoverEntry(r.entries, filepath.Dir(opfPath), opf.coverHref)
}

// parseEPUBReader 从已打开的 zip 解析章节与封面。
func parseEPUBReader(er *epubZipReader) ([]epubChapter, string, error) {
	readEntry := er.read

	opfPath, err := epubOPFPath(readEntry)
	if err != nil {
		return nil, "", err
	}
	opfRaw, err := readEntry(opfPath)
	if err != nil {
		return nil, "", err
	}
	opf, err := parseOPF(opfRaw)
	if err != nil {
		return nil, "", err
	}
	opfDir := filepath.Dir(opfPath)

	cover := epubCoverEntry(er.entries, opfDir, opf.coverHref)

	// href（相对 OPF 目录）→ 标题
	titles := map[string]string{}
	for _, href := range []string{opf.ncxHref, opf.navHref} {
		if href == "" {
			continue
		}
		raw, err := readEntry(filepath.Join(opfDir, href))
		if err != nil {
			continue
		}
		for k, v := range parseEPUBTitles(raw) {
			full := filepath.Clean(filepath.Join(opfDir, k))
			if _, ok := titles[full]; !ok {
				titles[full] = v
			}
		}
	}

	out := make([]epubChapter, 0, len(opf.spine))
	for _, href := range opf.spine {
		full := filepath.Clean(filepath.Join(opfDir, href))
		raw, err := readEntry(filepath.Join(opfDir, href))
		if err != nil {
			continue
		}
		title := titles[full]
		if title == "" {
			// NCX/NAV 里没有这页：正文首行 → 封面页 → <title> → 序号
			body := epubHTMLToText(string(raw), filepath.Dir(full))
			title = firstHeading(body)
			if title == "" && coverLikeEntry(full) && strings.Contains(body, imgMarkerPrefix) {
				title = "封面"
			}
			if title == "" {
				title = meaningfulTitle(htmlHeadTitle(string(raw)))
			}
			if title == "" {
				title = fmt.Sprintf("第 %d 章", len(out)+1)
			}
		}
		out = append(out, epubChapter{Title: title, Href: full})
	}
	return out, cover, nil
}

// epubCoverEntry 定位封面图在 zip 里的条目路径：
// 优先用 OPF 声明（EPUB3 manifest 的 properties="cover-image" 或 EPUB2 的
// <meta name="cover">）；声明缺失或指向不存在的条目时，退回文件名含 cover 的图片。
func epubCoverEntry(entries map[string]*zip.File, opfDir, declaredHref string) string {
	if declaredHref != "" {
		full := filepath.Clean(filepath.Join(opfDir, declaredHref))
		if f, ok := entries[full]; ok && isImageEntry(f.Name) {
			return full
		}
	}
	// 兜底：名字像封面（cover.jpg / 封面.png / images/cover.jpeg）的图片
	var fallback string
	for name := range entries {
		base := strings.ToLower(filepath.Base(name))
		if !strings.Contains(base, "cover") && !strings.Contains(base, "封面") {
			continue
		}
		if !isImageEntry(name) {
			continue
		}
		// 多个候选时取路径最短的，通常就是主封面
		if fallback == "" || len(name) < len(fallback) {
			fallback = name
		}
	}
	return fallback
}

// isImageEntry 判断 zip 条目是不是图片。
func isImageEntry(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".avif", ".svg":
		return true
	}
	return false
}

// coverLikeEntry 文件名像封面页（cover1.html / cover.xhtml / 封面.jpg 等）。
func coverLikeEntry(entry string) bool {
	name := strings.ToLower(filepath.Base(entry))
	return strings.Contains(name, "cover") || strings.Contains(name, "封面")
}

// meaningfulTitle 过滤掉封面/版权页那类无信息量的 <title>。
func meaningfulTitle(title string) string {
	switch strings.ToLower(strings.TrimSpace(title)) {
	case "", "cover", "table of contents", "contents", "unknown", "untitled", "title":
		return ""
	}
	return title
}

// epubOPFPath 从 META-INF/container.xml 找 OPF 路径。
func epubOPFPath(readEntry func(string) ([]byte, error)) (string, error) {
	raw, err := readEntry("META-INF/container.xml")
	if err != nil {
		return "", fmt.Errorf("EPUB 缺少 META-INF/container.xml")
	}
	var c struct {
		Rootfiles []struct {
			FullPath string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if err := xml.Unmarshal(raw, &c); err != nil {
		return "", fmt.Errorf("解析 container.xml 失败: %w", err)
	}
	for _, rf := range c.Rootfiles {
		if p := strings.TrimSpace(rf.FullPath); p != "" {
			return filepath.Clean(p), nil
		}
	}
	return "", fmt.Errorf("container.xml 里没有 rootfile")
}

type epubPackage struct {
	spine     []string
	ncxHref   string
	navHref   string
	coverHref string // 封面图 href（相对 OPF 目录），取不到为空
}

// parseOPF 解析 OPF：manifest 建 id→href 索引，spine 定顺序，顺带找出 NCX/NAV 目录
// 与封面图。
func parseOPF(raw []byte) (*epubPackage, error) {
	var pkg struct {
		Metadata struct {
			Metas []struct {
				Name    string `xml:"name,attr"`
				Content string `xml:"content,attr"`
			} `xml:"meta"`
		} `xml:"metadata"`
		Manifest struct {
			Items []struct {
				ID         string `xml:"id,attr"`
				Href       string `xml:"href,attr"`
				MediaType  string `xml:"media-type,attr"`
				Properties string `xml:"properties,attr"`
			} `xml:"item"`
		} `xml:"manifest"`
		Spine struct {
			Toc      string `xml:"toc,attr"`
			ItemRefs []struct {
				IDRef string `xml:"idref,attr"`
			} `xml:"itemref"`
		} `xml:"spine"`
	}
	if err := xml.Unmarshal(raw, &pkg); err != nil {
		return nil, fmt.Errorf("解析 OPF 失败: %w", err)
	}
	hrefByID := map[string]string{}
	out := &epubPackage{}
	for _, it := range pkg.Manifest.Items {
		if it.ID != "" {
			hrefByID[it.ID] = it.Href
		}
		switch {
		case strings.Contains(it.Properties, "nav"):
			out.navHref = it.Href
		case strings.EqualFold(it.MediaType, "application/x-dtbncx+xml"):
			if out.ncxHref == "" {
				out.ncxHref = it.Href
			}
		case strings.Contains(it.Properties, "cover-image"):
			// EPUB3 封面
			if out.coverHref == "" {
				out.coverHref = it.Href
			}
		}
	}
	// EPUB2 封面：<meta name="cover" content="封面 item 的 id"/>
	if out.coverHref == "" {
		for _, m := range pkg.Metadata.Metas {
			if !strings.EqualFold(strings.TrimSpace(m.Name), "cover") {
				continue
			}
			if href := hrefByID[strings.TrimSpace(m.Content)]; href != "" {
				out.coverHref = href
				break
			}
		}
	}
	if out.ncxHref == "" && pkg.Spine.Toc != "" {
		out.ncxHref = hrefByID[pkg.Spine.Toc]
	}
	for _, ref := range pkg.Spine.ItemRefs {
		href := hrefByID[ref.IDRef]
		if href == "" {
			continue
		}
		if !strings.HasPrefix(href, "#") {
			out.spine = append(out.spine, href)
		}
	}
	if len(out.spine) == 0 {
		return nil, fmt.Errorf("EPUB spine 为空")
	}
	return out, nil
}

// parseEPUBTitles 从 NCX（navPoint 树）或 EPUB3 NAV（nav 里的 a）取 相对href → 标题。
//
// 用 token 逐个走而不是结构体映射：网上不少 EPUB（如 Epubor 导出的）navPoint
// 漏了闭合标签，标题会整棵挂在上一个 navPoint 里面，按固定层级解会丢掉一大半；
// 这里用栈把任意层级的 navPoint 都收下来。
func parseEPUBTitles(raw []byte) map[string]string {
	out := map[string]string{}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var (
		stack    []navItem
		labelDep int  // >0 表示在 navLabel 内
		navDep   int  // >0 表示在 <nav> 内
		inAnchor bool // 正在读 <a>…</a>
		href     string
		text     strings.Builder
	)
	pop := func() {
		if n := len(stack); n > 0 {
			it := stack[n-1]
			stack = stack[:n-1]
			putTitle(out, it.src, it.text)
		}
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "navPoint":
				stack = append(stack, navItem{})
			case "navLabel":
				labelDep++
			case "text":
				if labelDep > 0 && len(stack) > 0 && stack[len(stack)-1].text == "" {
					var s string
					if dec.DecodeElement(&s, &t) == nil {
						stack[len(stack)-1].text = strings.TrimSpace(s)
					}
					continue
				}
			case "content":
				if len(stack) > 0 && stack[len(stack)-1].src == "" {
					for _, a := range t.Attr {
						if a.Name.Local == "src" {
							stack[len(stack)-1].src = a.Value
						}
					}
				}
			case "nav":
				navDep++
			case "a":
				if navDep > 0 {
					inAnchor, href = true, ""
					text.Reset()
					for _, a := range t.Attr {
						if a.Name.Local == "href" {
							href = a.Value
						}
					}
				}
			}
		case xml.CharData:
			if inAnchor {
				text.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "navLabel":
				if labelDep > 0 {
					labelDep--
				}
			case "navPoint":
				pop()
			case "nav":
				if navDep > 0 {
					navDep--
				}
			case "a":
				if inAnchor {
					inAnchor = false
					putTitle(out, href, strings.TrimSpace(text.String()))
				}
			}
		}
	}
	// 兜底：文件尾还有没闭合的 navPoint
	for range stack {
		pop()
	}
	return out
}

// navItem NCX 里的一个 navPoint。
type navItem struct{ src, text string }

// putTitle 记录 href → 标题（去掉锚点、去重，先到先得）。
func putTitle(out map[string]string, href, title string) {
	href = strings.TrimSpace(href)
	if i := strings.IndexByte(href, '#'); i >= 0 {
		href = href[:i]
	}
	if href == "" || title == "" {
		return
	}
	if _, ok := out[href]; !ok {
		out[href] = title
	}
}

// readEPUBEntry 读 zip 内某个 XHTML 并转纯文本（图片转成 [img] 标记行）。
func readEPUBEntry(epubPath, href string) (string, error) {
	zr, err := zip.OpenReader(epubPath) // #nosec G304 -- 服务端生成的路径
	if err != nil {
		return "", fmt.Errorf("本地书籍文件已丢失: %w", err)
	}
	defer zr.Close()
	target := filepath.Clean(href)
	for _, f := range zr.File {
		if filepath.Clean(f.Name) != target {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		defer rc.Close()
		raw, err := io.ReadAll(io.LimitReader(rc, LocalBookMaxBytes))
		if err != nil {
			return "", err
		}
		return epubHTMLToText(string(raw), filepath.Dir(target)), nil
	}
	return "", fmt.Errorf("EPUB 条目不存在，请重新导入该书")
}

// ReadLocalAsset 读取本地书籍里的资源（目前用于 EPUB 图片）。
// 返回内容与 MIME 类型。
func (s *ReaderService) ReadLocalAsset(ctx context.Context, bookID, entry string) ([]byte, string, error) {
	book, err := s.repo.GetBook(ctx, bookID)
	if err != nil {
		return nil, "", err
	}
	if !strings.HasSuffix(book.LocalPath, ".epub") {
		return nil, "", fmt.Errorf("该书籍没有内嵌资源")
	}
	path, err := s.localFilePath(book)
	if err != nil {
		return nil, "", err
	}
	zr, err := zip.OpenReader(path) // #nosec G304 -- 服务端生成的路径
	if err != nil {
		return nil, "", fmt.Errorf("本地书籍文件已丢失: %w", err)
	}
	defer zr.Close()
	target := filepath.Clean(entry)
	for _, f := range zr.File {
		if filepath.Clean(f.Name) != target {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, "", err
		}
		defer rc.Close()
		data, err := io.ReadAll(io.LimitReader(rc, LocalBookMaxBytes))
		if err != nil {
			return nil, "", err
		}
		return data, imageContentType(entry), nil
	}
	return nil, "", fmt.Errorf("EPUB 内没有该资源")
}

// imageContentType 按扩展名给图片类型（不依赖系统 mime 注册表）。
func imageContentType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".bmp":
		return "image/bmp"
	case ".avif":
		return "image/avif"
	}
	return "application/octet-stream"
}

// imgTagPattern 匹配 <img ...>，取 src。
var imgTagPattern = regexp.MustCompile(`(?is)<img\b[^>]*?src\s*=\s*("([^"]*)"|'([^']*)')[^>]*>`)

// imgMarkerPrefix 图片在正文里的占位行前缀，前端据此渲染 <img>。
const imgMarkerPrefix = "[img]"

// epubHTMLToText EPUB 正文转纯文本：先把 <img> 换成 [img]条目路径 标记行，
// 再按普通 HTML 去标签。baseDir 是当前 XHTML 所在目录，用来解析相对路径。
func epubHTMLToText(src, baseDir string) string {
	src = imgTagPattern.ReplaceAllStringFunc(src, func(tag string) string {
		m := imgTagPattern.FindStringSubmatch(tag)
		if m == nil {
			return ""
		}
		ref := strings.TrimSpace(m[2] + m[3])
		if ref == "" {
			return ""
		}
		ref = strings.ReplaceAll(html.UnescapeString(ref), "\\", "/")
		// data: / 外链图片原样保留；相对路径按当前文件目录解析成 zip 条目
		if !strings.Contains(ref, "://") && !strings.HasPrefix(ref, "data:") {
			if i := strings.IndexByte(ref, '#'); i >= 0 {
				ref = ref[:i]
			}
			ref = filepath.Clean(filepath.Join(baseDir, filepath.FromSlash(ref)))
		}
		return "\n" + imgMarkerPrefix + ref + "\n"
	})
	return htmlToText(src)
}

// htmlHeadTitle 取 <head><title> 文本（EPUB 里常与正文标题一致）。
func htmlHeadTitle(src string) string {
	m := headTitlePattern.FindStringSubmatch(src)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(m[1]))
}

var headTitlePattern = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title\s*>`)

// rewriteLocalImages 把正文里的 [img]条目 标记换成签名资源地址。
func (s *ReaderService) rewriteLocalImages(bookID, text string) string {
	if !strings.Contains(text, imgMarkerPrefix) {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, imgMarkerPrefix) {
			continue
		}
		entry := strings.TrimSpace(strings.TrimPrefix(line, imgMarkerPrefix))
		if entry == "" || strings.HasPrefix(entry, "/api/") || strings.Contains(entry, "://") {
			continue
		}
		lines[i] = imgMarkerPrefix + s.LocalAssetURL(bookID, entry)
	}
	return strings.Join(lines, "\n")
}

// signLocalAsset 本地书籍资源的 HMAC 签名（与媒体代理同一套：<img> 带不了 JWT）。
func (s *ReaderService) signLocalAsset(bookID, entry string) string {
	mac := hmac.New(sha256.New, []byte(s.cfg.Secrets.JWTSecret))
	mac.Write([]byte(bookID + "|local|" + entry))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

// LocalAssetURL 本地书籍内资源转签名代理地址。
func (s *ReaderService) LocalAssetURL(bookID, entry string) string {
	return "/api/reader/local/asset?b=" + url.QueryEscape(bookID) +
		"&p=" + base64.RawURLEncoding.EncodeToString([]byte(entry)) +
		"&s=" + s.signLocalAsset(bookID, entry)
}

// VerifyLocalAssetURL 校验签名并还原条目路径。
func (s *ReaderService) VerifyLocalAssetURL(bookID, encoded, sig string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("资源路径解码失败")
	}
	expect := s.signLocalAsset(bookID, string(raw))
	if !hmac.Equal([]byte(expect), []byte(sig)) {
		return "", fmt.Errorf("资源签名校验失败")
	}
	return string(raw), nil
}

// signLocalAudio 本地有声书音频文件的 HMAC 签名（与本地资源同一套：<audio> 带不了 JWT）。
func (s *ReaderService) signLocalAudio(bookID, path string) string {
	mac := hmac.New(sha256.New, []byte(s.cfg.Secrets.JWTSecret))
	mac.Write([]byte(bookID + "|audio|" + path))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

// LocalAudioURL 本地音频文件转签名流地址（服务端按 Range 下发，可拖动进度）。
func (s *ReaderService) LocalAudioURL(bookID, path string) string {
	return "/api/reader/local/audio?b=" + url.QueryEscape(bookID) +
		"&p=" + base64.RawURLEncoding.EncodeToString([]byte(path)) +
		"&s=" + s.signLocalAudio(bookID, path)
}

// VerifyLocalAudioURL 校验签名并还原音频文件路径。
func (s *ReaderService) VerifyLocalAudioURL(bookID, encoded, sig string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("音频路径解码失败")
	}
	expect := s.signLocalAudio(bookID, string(raw))
	if !hmac.Equal([]byte(expect), []byte(sig)) {
		return "", fmt.Errorf("音频签名校验失败")
	}
	return string(raw), nil
}

// firstHeading 取正文首个非空行当标题（跳过图片占位行）。
func firstHeading(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, imgMarkerPrefix) {
			continue
		}
		if r := []rune(line); len(r) > 40 {
			return string(r[:40])
		}
		return line
	}
	return ""
}

// htmlToText 把 XHTML 转成纯文本：丢 script/style、块级标签转换行、解码实体。
func htmlToText(src string) string {
	src = regexp.MustCompile(`(?is)<script\b.*?</script\s*>`).ReplaceAllString(src, "")
	src = regexp.MustCompile(`(?is)<style\b.*?</style\s*>`).ReplaceAllString(src, "")
	src = regexp.MustCompile(`(?is)<head\b.*?</head\s*>`).ReplaceAllString(src, "")
	src = regexp.MustCompile(`(?i)<\s*br\s*/?\s*>`).ReplaceAllString(src, "\n")
	src = regexp.MustCompile(`(?i)</\s*(p|div|h[1-6]|li|tr|blockquote|section|article)\s*>`).ReplaceAllString(src, "\n")
	src = regexp.MustCompile(`(?s)<[^>]*>`).ReplaceAllString(src, "")
	src = html.UnescapeString(src)
	src = strings.ReplaceAll(src, "\u00a0", " ")
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
