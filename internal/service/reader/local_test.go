package reader

import (
	"archive/zip"
	"bytes"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件：本地书籍（TXT / EPUB）导入与阅读的回归测试。

// newLocalBookService 复用登录测试的建库逻辑，另给一个临时 DataDir，
// 避免本地书籍文件写到仓库目录里。
func newLocalBookService(t *testing.T) *ReaderService {
	t.Helper()
	svc, _ := newLoginTestService(t)
	svc.cfg.App.DataDir = t.TempDir()
	return svc
}

const sampleTXT = `书名：测试小说
作者：某某

第1章 开端
第一章的正文内容。
这里还有一行。

第2章 发展
第二章的正文内容。

第3章 结局
第三章的正文内容。
`

func TestSplitTXTChapters(t *testing.T) {
	chapters := splitTXTChapters(sampleTXT)
	if len(chapters) != 3 {
		t.Fatalf("章节数 = %d，期望 3：%+v", len(chapters), chapters)
	}
	wantTitles := []string{"第1章 开端", "第2章 发展", "第3章 结局"}
	for i, c := range chapters {
		if c.Title != wantTitles[i] {
			t.Errorf("第 %d 章标题 = %q，期望 %q", i+1, c.Title, wantTitles[i])
		}
	}
	// 第一章要从文件头开始，把书名/作者并进去
	if chapters[0].Start != 0 {
		t.Errorf("第一章起点 = %d，期望 0", chapters[0].Start)
	}
	if chapters[len(chapters)-1].End != len(sampleTXT) {
		t.Errorf("末章终点 = %d，期望 %d", chapters[len(chapters)-1].End, len(sampleTXT))
	}
	// 区间必须首尾相接且不重叠
	for i := 1; i < len(chapters); i++ {
		if chapters[i].Start != chapters[i-1].End {
			t.Errorf("第 %d/%d 章区间不连续：%d vs %d", i, i+1, chapters[i-1].End, chapters[i].Start)
		}
	}
	// 每章正文要能切出来且包含该章内容
	body := sampleTXT[chapters[1].Start:chapters[1].End]
	if !strings.Contains(body, "第二章的正文内容") {
		t.Errorf("第二章区间内容不对: %q", body)
	}
	if strings.Contains(body, "第三章") {
		t.Errorf("第二章区间串到了第三章: %q", body)
	}
}

func TestSplitTXTChaptersFallback(t *testing.T) {
	chapters := splitTXTChapters("没有任何章节标记的一段文字。\n第二行。")
	if len(chapters) != 1 || chapters[0].Title != "全文" {
		t.Fatalf("无章标记应整本当一章: %+v", chapters)
	}
	if chapters[0].Start != 0 || chapters[0].End != len("没有任何章节标记的一段文字。\n第二行。") {
		t.Fatalf("整本区间不对: %+v", chapters[0])
	}
}

func TestDecodeTextFileGBK(t *testing.T) {
	// "第1章 开端" 的 GBK 编码
	gbk := []byte{0xB5, 0xDA, '1', 0xD5, 0xC2, ' ', 0xBF, 0xAA, 0xB6, 0xCB}
	got, charset := decodeTextFile(gbk)
	if charset != "gbk" {
		t.Fatalf("字符集 = %q，期望 gbk", charset)
	}
	if got != "第1章 开端" {
		t.Fatalf("解码结果 = %q", got)
	}
}

func TestImportLocalBookAndRead(t *testing.T) {
	svc := newLocalBookService(t)
	ctx := t.Context()

	book, err := svc.ImportLocalBook(ctx, "u1", "测试小说.txt", []byte(sampleTXT))
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if !book.IsLocal || book.LocalPath == "" {
		t.Fatalf("导入后应标记为本地书籍: %+v", book)
	}
	if book.Name != "测试小说" {
		t.Errorf("书名 = %q", book.Name)
	}
	if book.TotalChapterNum != 3 {
		t.Errorf("总章数 = %d，期望 3", book.TotalChapterNum)
	}

	chapters, err := svc.ListChapters(ctx, book.ID)
	if err != nil || len(chapters) != 3 {
		t.Fatalf("目录 = %d 章, err=%v", len(chapters), err)
	}

	got, err := svc.LocalChapterContent(ctx, "u1", book.ID, 1)
	if err != nil {
		t.Fatalf("读第二章失败: %v", err)
	}
	if got.Type != "text" {
		t.Errorf("type = %q", got.Type)
	}
	if !strings.Contains(got.Content, "第二章的正文内容") {
		t.Errorf("第二章正文 = %q", got.Content)
	}
	if strings.Contains(got.Content, "第三章") || strings.Contains(got.Content, "第一章的正文") {
		t.Errorf("第二章正文串章了: %q", got.Content)
	}
	// 书名等前言应落在第一章
	first, err := svc.LocalChapterContent(ctx, "u1", book.ID, 0)
	if err != nil {
		t.Fatalf("读第一章失败: %v", err)
	}
	if !strings.Contains(first.Content, "第一章的正文内容") {
		t.Errorf("第一章正文 = %q", first.Content)
	}

	// 越界与越权
	if _, err := svc.LocalChapterContent(ctx, "u1", book.ID, 9); err == nil {
		t.Error("越界章节应报错")
	}
	if _, err := svc.LocalChapterContent(ctx, "other", book.ID, 0); err == nil {
		t.Error("他人书架应拒绝")
	}

	// 书架列表要带 is_local
	books, err := svc.ListBooks(ctx, "u1")
	if err != nil || len(books) != 1 || !books[0].IsLocal {
		t.Fatalf("书架列表未标记本地书籍: %+v, err=%v", books, err)
	}

	// 重复导入同名文件：覆盖更新且保留进度
	if err := svc.SaveProgress(ctx, "u1", book.ID, 1, 0, "第2章 发展"); err != nil {
		t.Fatal(err)
	}
	again, err := svc.ImportLocalBook(ctx, "u1", "测试小说.txt", []byte(sampleTXT))
	if err != nil {
		t.Fatalf("重复导入失败: %v", err)
	}
	if again.ID != book.ID {
		t.Errorf("同名导入应覆盖同一本书，得到新 ID %s", again.ID)
	}
	if again.DurChapterIndex != 1 {
		t.Errorf("章节数不变时应保留进度，得到 %d", again.DurChapterIndex)
	}

	// 移出书架要删掉落盘文件
	path, err := svc.localFilePath(again)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveBook(ctx, "u1", again.ID); err != nil {
		t.Fatalf("移出书架失败: %v", err)
	}
	if _, err := os.ReadFile(path); err == nil {
		t.Error("移出书架后本地文件应被删除")
	}
}

// epubFixture 造一个最小可用的 EPUB（NCX 目录 + 两个 XHTML 章节）。
func epubFixture(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("mimetype", "application/epub+zip")
	add("META-INF/container.xml", `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`)
	add("OEBPS/content.opf", `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <manifest>
    <item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>
    <item id="c1" href="ch1.xhtml" media-type="application/xhtml+xml"/>
    <item id="c2" href="ch2.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine toc="ncx">
    <itemref idref="c1"/><itemref idref="c2"/>
  </spine>
</package>`)
	add("OEBPS/toc.ncx", `<?xml version="1.0"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
  <navMap>
    <navPoint id="n1"><navLabel><text>第一章 起风</text></navLabel><content src="ch1.xhtml"/></navPoint>
    <navPoint id="n2"><navLabel><text>第二章 落雨</text></navLabel><content src="ch2.xhtml"/></navPoint>
  </navMap>
</ncx>`)
	add("OEBPS/ch1.xhtml", `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>t</title></head>
<body><h1>第一章 起风</h1><p>第一段 &amp; 实体。</p><p>第二段。</p><script>var x=1;</script></body></html>`)
	add("OEBPS/ch2.xhtml", `<html xmlns="http://www.w3.org/1999/xhtml"><body><p>第二章正文。</p><br/><p>又一段。</p></body></html>`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImportEPUBAndRead(t *testing.T) {
	svc := newLocalBookService(t)
	ctx := t.Context()

	book, err := svc.ImportLocalBook(ctx, "u1", "测试电子书.epub", epubFixture(t))
	if err != nil {
		t.Fatalf("导入 EPUB 失败: %v", err)
	}
	if book.TotalChapterNum != 2 {
		t.Fatalf("章数 = %d，期望 2", book.TotalChapterNum)
	}
	chapters, err := svc.ListChapters(ctx, book.ID)
	if err != nil || len(chapters) != 2 {
		t.Fatalf("目录 = %+v, err=%v", chapters, err)
	}
	if chapters[0].Title != "第一章 起风" || chapters[1].Title != "第二章 落雨" {
		t.Fatalf("章节标题应取自 NCX: %q / %q", chapters[0].Title, chapters[1].Title)
	}
	// 标题不要留在正文里
	first, err := svc.LocalChapterContent(ctx, "u1", book.ID, 0)
	if err != nil {
		t.Fatalf("读第一章失败: %v", err)
	}
	if !strings.Contains(first.Content, "第一段 & 实体。") {
		t.Errorf("实体未解码: %q", first.Content)
	}
	if !strings.Contains(first.Content, "第一段 & 实体。\n第二段。") {
		t.Errorf("段落换行不对: %q", first.Content)
	}
	if strings.Contains(first.Content, "var x=1") {
		t.Errorf("script 应被剔除: %q", first.Content)
	}
	second, err := svc.LocalChapterContent(ctx, "u1", book.ID, 1)
	if err != nil || !strings.Contains(second.Content, "第二章正文。") {
		t.Fatalf("读第二章失败: %v / %q", err, second.Content)
	}
}

// TestImportEPUBWithImage 端到端：导入带图片的 EPUB，章节正文里图片应变成签名
// 地址，且该地址能取回原始图片字节。
func TestImportEPUBWithImage(t *testing.T) {
	svc := newLocalBookService(t)
	ctx := t.Context()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	imgBytes := []byte("\xff\xd8\xff\xe0fakejpeg")
	add("mimetype", "application/epub+zip")
	add("META-INF/container.xml", `<?xml version="1.0"?><container version="1.0"
  xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles>
  <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`)
	add("OEBPS/content.opf", `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <manifest>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="c1" href="ch1.xhtml" media-type="application/xhtml+xml"/>
    <item id="i1" href="images/pic.jpg" media-type="image/jpeg"/>
  </manifest>
  <spine><itemref idref="c1"/></spine></package>`)
	add("OEBPS/nav.xhtml", `<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
  <body><nav epub:type="toc"><ol><li><a href="ch1.xhtml">插图章</a></li></ol></nav></body></html>`)
	add("OEBPS/ch1.xhtml", `<html xmlns="http://www.w3.org/1999/xhtml"><body><h1>插图章</h1>
  <p>图片前。</p><img src="images/pic.jpg" alt=""/><p>图片后。</p></body></html>`)
	w, err := zw.Create("OEBPS/images/pic.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(imgBytes); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	book, err := svc.ImportLocalBook(ctx, "u1", "带图.epub", buf.Bytes())
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	chapters, err := svc.ListChapters(ctx, book.ID)
	if err != nil || len(chapters) != 1 || chapters[0].Title != "插图章" {
		t.Fatalf("目录 = %+v, err=%v", chapters, err)
	}
	content, err := svc.LocalChapterContent(ctx, "u1", book.ID, 0)
	if err != nil {
		t.Fatalf("读正文失败: %v", err)
	}
	if !strings.Contains(content.Content, "图片前。") || !strings.Contains(content.Content, "图片后。") {
		t.Fatalf("正文缺内容: %q", content.Content)
	}
	var asset string
	for _, line := range strings.Split(content.Content, "\n") {
		if strings.HasPrefix(line, imgMarkerPrefix) {
			asset = strings.TrimPrefix(line, imgMarkerPrefix)
		}
	}
	if !strings.HasPrefix(asset, "/api/reader/local/asset?") {
		t.Fatalf("图片未换成签名地址: %q（正文 %q）", asset, content.Content)
	}
	u, err := url.Parse(asset)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := svc.VerifyLocalAssetURL(u.Query().Get("b"), u.Query().Get("p"), u.Query().Get("s"))
	if err != nil {
		t.Fatalf("资源签名校验失败: %v", err)
	}
	data, ct, err := svc.ReadLocalAsset(ctx, book.ID, entry)
	if err != nil {
		t.Fatalf("读图片失败: %v", err)
	}
	if !bytes.Equal(data, imgBytes) {
		t.Fatalf("图片字节不一致: %q", data)
	}
	if ct != "image/jpeg" {
		t.Fatalf("Content-Type = %q", ct)
	}
}

func TestHTMLToText(t *testing.T) {
	got := htmlToText("<div>甲</div>\n<p>乙<br/>丙</p><style>p{}</style>")
	if got != "甲\n乙\n丙" {
		t.Fatalf("htmlToText = %q", got)
	}
}

// TestParseEPUBTitlesHandlesNestedNavPoint 网上不少 EPUB（Epubor 导出）navPoint
// 漏了闭合标签，标题会整棵挂进上一个 navPoint。按固定层级解会丢一大半标题，
// 这里要求深层节点也能收全。
func TestParseEPUBTitlesHandlesNestedNavPoint(t *testing.T) {
	raw := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
  <docTitle><text>UnKnown</text></docTitle>
  <navMap>
    <navPoint id="id1"><navLabel><text>目录</text></navLabel><content src="text00000.html"/></navPoint>
    <navPoint id="id2"><navLabel><text>小狗钱钱1</text></navLabel><content src="text00002.html"/>
    <navPoint id="id3"><navLabel><text>童话与理财</text></navLabel><content src="text00005.html"/></navPoint>
    <navPoint id="id4"><navLabel><text>前言</text></navLabel><content src="text00006.html"/></navPoint>
    </navPoint>
  </navMap>
</ncx>`)
	titles := parseEPUBTitles(raw)
	want := map[string]string{
		"text00000.html": "目录",
		"text00002.html": "小狗钱钱1",
		"text00005.html": "童话与理财",
		"text00006.html": "前言",
	}
	for href, title := range want {
		if titles[href] != title {
			t.Errorf("%s 标题 = %q，期望 %q（全部：%v）", href, titles[href], title, titles)
		}
	}
	if titles["UnKnown"] != "" {
		t.Errorf("docTitle 不该被当成章节标题：%v", titles)
	}
}

// TestParseEPUBTitlesNav 兼容 EPUB3 的 nav 目录（含带锚点与嵌套 ol 的情况）。
func TestParseEPUBTitlesNav(t *testing.T) {
	raw := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<body>
<nav epub:type="toc"><h1>目录</h1><ol>
  <li><a href="ch1.xhtml">第一章 起风</a></li>
  <li><a href="ch2.xhtml#s1">第二章 落雨</a>
    <ol><li><a href="ch3.xhtml">第三章 天晴</a></li></ol>
  </li>
</ol></nav>
</body></html>`)
	titles := parseEPUBTitles(raw)
	for href, title := range map[string]string{
		"ch1.xhtml": "第一章 起风",
		"ch2.xhtml": "第二章 落雨",
		"ch3.xhtml": "第三章 天晴",
	} {
		if titles[href] != title {
			t.Errorf("%s 标题 = %q，期望 %q（全部：%v）", href, titles[href], title, titles)
		}
	}
}

// TestEpubHTMLToTextWithImages 图片要变成 [img]条目 标记行，且相对路径按正文所在
// 目录解析；标记行不能带缩进（前端据此渲染图片）。
func TestEpubHTMLToTextWithImages(t *testing.T) {
	src := `<html><body><h1>第一章</h1>
<p>正文一。</p>
<img src="Image00024.jpg" style="width:100%;height:100%;" />
<p>正文二<img src='sub/pic.png'/>尾巴。</p>
<p><img src="http://cdn.example.com/a.jpg"/></p>
</body></html>`
	got := epubHTMLToText(src, "OEBPS")
	want := "第一章\n正文一。\n" + imgMarkerPrefix + filepath.Join("OEBPS", "Image00024.jpg") +
		"\n正文二\n" + imgMarkerPrefix + filepath.Join("OEBPS", "sub", "pic.png") + "\n尾巴。\n" +
		imgMarkerPrefix + "http://cdn.example.com/a.jpg"
	if got != want {
		t.Fatalf("epubHTMLToText =\n%q\n期望\n%q", got, want)
	}
}

// TestRewriteLocalImages 标记里的条目要换成签名地址，且重复处理不会二次改写。
func TestRewriteLocalImages(t *testing.T) {
	svc := newLocalBookService(t)
	text := imgMarkerPrefix + filepath.Join("OEBPS", "a.jpg") + "\n正文\n" + imgMarkerPrefix + "http://x/b.png"
	out := svc.rewriteLocalImages("book-1", text)
	lines := strings.Split(out, "\n")
	if !strings.HasPrefix(lines[0], imgMarkerPrefix+"/api/reader/local/asset?b=book-1&p=") {
		t.Fatalf("相对路径未改写：%q", lines[0])
	}
	if !strings.HasSuffix(lines[2], "http://x/b.png") {
		t.Fatalf("外链图片不该改写：%q", lines[2])
	}
	// 再跑一次应保持不变
	if again := svc.rewriteLocalImages("book-1", out); again != out {
		t.Fatalf("重复改写改变了结果：%q", again)
	}
	// 签名可校验，改一个字符就不认
	entry := filepath.Join("OEBPS", "a.jpg")
	u := svc.LocalAssetURL("book-1", entry)
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.VerifyLocalAssetURL("book-1", parsed.Query().Get("p"), parsed.Query().Get("s"))
	if err != nil || got != entry {
		t.Fatalf("签名校验失败: %v / %q", err, got)
	}
	if _, err := svc.VerifyLocalAssetURL("book-1", parsed.Query().Get("p"), "deadbeef"); err == nil {
		t.Fatal("错误签名应校验失败")
	}
	if _, err := svc.VerifyLocalAssetURL("book-2", parsed.Query().Get("p"), parsed.Query().Get("s")); err == nil {
		t.Fatal("换一本书后签名应失效")
	}
}

// epubWithCoverFixture 造一个带封面的最小 EPUB。opfCover 决定封面怎么写：
// EPUB3 用 properties="cover-image"，EPUB2 用 <meta name="cover">；都为 false
// 时封面只能靠文件名兜底。
func epubWithCoverFixture(t *testing.T, epub3, epub2 bool) ([]byte, []byte) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	coverBytes := []byte("\xff\xd8\xff\xe0fakecoverjpeg")
	add("mimetype", "application/epub+zip")
	add("META-INF/container.xml", `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`)
	meta := ""
	if epub2 {
		meta = `<metadata><meta name="cover" content="cover-img"/></metadata>`
	}
	props := ""
	if epub3 {
		props = ` properties="cover-image"`
	}
	add("OEBPS/content.opf", `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">`+meta+`
  <manifest>
    <item id="c1" href="ch1.xhtml" media-type="application/xhtml+xml"/>
    <item id="cover-img" href="images/cover.jpg" media-type="image/jpeg"`+props+`/>
  </manifest>
  <spine><itemref idref="c1"/></spine>
</package>`)
	add("OEBPS/ch1.xhtml", `<html xmlns="http://www.w3.org/1999/xhtml"><body><h1>正文</h1><p>内容。</p></body></html>`)
	w, err := zw.Create("OEBPS/images/cover.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(coverBytes); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), coverBytes
}

// TestImportEPUBCover 封面要能从 OPF 声明（EPUB3 / EPUB2）与文件名兜底三条路
// 解析出来，并转成可访问的签名资源地址。
func TestImportEPUBCover(t *testing.T) {
	ctx := t.Context()
	cases := []struct {
		name         string
		epub3, epub2 bool
	}{
		{"EPUB3 cover-image", true, false},
		{"EPUB2 meta cover", false, true},
		{"文件名兜底", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newLocalBookService(t)
			raw, coverBytes := epubWithCoverFixture(t, tc.epub3, tc.epub2)
			book, err := svc.ImportLocalBook(ctx, "u1", "有封面.epub", raw)
			if err != nil {
				t.Fatalf("导入失败: %v", err)
			}
			if !strings.HasPrefix(book.CoverURL, "/api/reader/local/asset?") {
				t.Fatalf("封面未生成签名地址: %q", book.CoverURL)
			}
			u, err := url.Parse(book.CoverURL)
			if err != nil {
				t.Fatal(err)
			}
			entry, err := svc.VerifyLocalAssetURL(u.Query().Get("b"), u.Query().Get("p"), u.Query().Get("s"))
			if err != nil {
				t.Fatalf("封面签名校验失败: %v", err)
			}
			data, ct, err := svc.ReadLocalAsset(ctx, book.ID, entry)
			if err != nil {
				t.Fatalf("读封面失败: %v", err)
			}
			if !bytes.Equal(data, coverBytes) {
				t.Fatalf("封面字节不一致: %q", data)
			}
			if ct != "image/jpeg" {
				t.Fatalf("封面 MIME = %q，期望 image/jpeg", ct)
			}

			// 落库后书架列表/详情也要带上封面，重开服务不丢
			books, err := svc.ListBooks(ctx, "u1")
			if err != nil || len(books) != 1 || books[0].CoverURL != book.CoverURL {
				t.Fatalf("书架封面 = %+v, err=%v", books, err)
			}
		})
	}
}

// TestBackfillLocalCoverOnListBooks 早期导入的本地 EPUB 没存封面，
// 加载书架时应该自动补上并落库，不必让用户重新导入。
func TestBackfillLocalCoverOnListBooks(t *testing.T) {
	svc := newLocalBookService(t)
	ctx := t.Context()

	raw, coverBytes := epubWithCoverFixture(t, true, false)
	book, err := svc.ImportLocalBook(ctx, "u1", "老书.epub", raw)
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	// 模拟修复前导入的旧数据：库里没有封面
	book.CoverURL = ""
	if err := svc.repo.UpdateBook(ctx, book); err != nil {
		t.Fatal(err)
	}

	books, err := svc.ListBooks(ctx, "u1")
	if err != nil || len(books) != 1 {
		t.Fatalf("书架 = %+v, err=%v", books, err)
	}
	restored := books[0].CoverURL
	if !strings.HasPrefix(restored, "/api/reader/local/asset?") {
		t.Fatalf("书架加载未回填封面: %q", restored)
	}

	// 已经写回数据库，下次不用再解析
	stored, err := svc.repo.GetBook(ctx, book.ID)
	if err != nil || stored.CoverURL != restored {
		t.Fatalf("封面未落库: %+v, err=%v", stored, err)
	}

	// 回填出来的地址必须真能取到封面图
	u, err := url.Parse(restored)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := svc.VerifyLocalAssetURL(u.Query().Get("b"), u.Query().Get("p"), u.Query().Get("s"))
	if err != nil {
		t.Fatalf("回填封面签名校验失败: %v", err)
	}
	data, _, err := svc.ReadLocalAsset(ctx, book.ID, entry)
	if err != nil || !bytes.Equal(data, coverBytes) {
		t.Fatalf("回填封面取回失败: %v / %q", err, data)
	}
}

// TestBackfillLocalCoverSkipsTXTAndCoveredBooks TXT 没有内嵌图片，已有封面的书
// 也不该被重复处理。
func TestBackfillLocalCoverSkipsTXTAndCoveredBooks(t *testing.T) {
	svc := newLocalBookService(t)
	ctx := t.Context()

	txt, err := svc.ImportLocalBook(ctx, "u1", "小说.txt", []byte(sampleTXT))
	if err != nil {
		t.Fatalf("导入 TXT 失败: %v", err)
	}
	if svc.BackfillLocalCover(ctx, txt) {
		t.Error("TXT 不该被回填封面")
	}

	raw, _ := epubWithCoverFixture(t, true, false)
	epub, err := svc.ImportLocalBook(ctx, "u1", "已带封面.epub", raw)
	if err != nil {
		t.Fatalf("导入 EPUB 失败: %v", err)
	}
	if svc.BackfillLocalCover(ctx, epub) {
		t.Error("已有封面的书不该被回填")
	}
}

// TestImportLocalBookFromPath 服务器选书：原地引用不复制，章节可读，
// 移出书架不删除源文件。
func TestImportLocalBookFromPath(t *testing.T) {
	svc := newLocalBookService(t)
	ctx := t.Context()

	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "服务器上的书.txt")
	if err := os.WriteFile(srcPath, []byte(sampleTXT), 0o640); err != nil {
		t.Fatal(err)
	}

	book, err := svc.ImportLocalBookFromPath(ctx, "u1", srcPath)
	if err != nil {
		t.Fatalf("从服务器路径导入失败: %v", err)
	}
	if !book.LocalExternal {
		t.Fatal("应标记为原地引用")
	}
	if book.LocalPath != srcPath {
		t.Fatalf("LocalPath = %q，期望源文件路径 %q", book.LocalPath, srcPath)
	}
	if book.TotalChapterNum != 3 {
		t.Fatalf("章节数 = %d，期望 3", book.TotalChapterNum)
	}
	if book.Name != "服务器上的书" {
		t.Fatalf("书名 = %q", book.Name)
	}

	content, err := svc.LocalChapterContent(ctx, "u1", book.ID, 0)
	if err != nil {
		t.Fatalf("读正文失败: %v", err)
	}
	if !strings.Contains(content.Content, "第一章的正文内容。") {
		t.Fatalf("正文不对: %q", content.Content)
	}

	// 不应把文件复制进 data/reader/local
	localDir, err := svc.localBooksDir()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(localDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("原地引用不该产生托管副本，目录里有 %d 个文件", len(entries))
	}

	// 移出书架只解除引用，源文件必须还在
	if err := svc.RemoveBook(ctx, "u1", book.ID); err != nil {
		t.Fatalf("移出书架失败: %v", err)
	}
	if _, err := os.Stat(srcPath); err != nil {
		t.Fatalf("原地引用的源文件被删除了: %v", err)
	}
}

// TestImportLocalBookFromPathRejectsUnsupported 只接受 TXT / EPUB，目录与超限文件要报错。
func TestImportLocalBookFromPathRejectsUnsupported(t *testing.T) {
	svc := newLocalBookService(t)
	ctx := t.Context()

	dir := t.TempDir()
	pdf := filepath.Join(dir, "book.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.4"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportLocalBookFromPath(ctx, "u1", pdf); err == nil {
		t.Fatal("PDF 不该被接受")
	}
	if _, err := svc.ImportLocalBookFromPath(ctx, "u1", dir); err == nil {
		t.Fatal("目录不该被接受")
	}
	if _, err := svc.ImportLocalBookFromPath(ctx, "u1", filepath.Join(dir, "nope.txt")); err == nil {
		t.Fatal("不存在的文件应报错")
	}
}

// TestImportLocalAudioDir 目录导入有声书：音频与 .strm 成为章节，按相对路径排序，
// 其它文件忽略；.strm 指向远端走媒体代理，指向本地文件走本地音频端点。
func TestImportLocalAudioDir(t *testing.T) {
	svc := newLocalBookService(t)
	ctx := t.Context()

	base := t.TempDir()
	dir := filepath.Join(base, "有声书")
	shared := filepath.Join(base, "共享")
	for _, d := range []string{filepath.Join(dir, "00-cd1"), shared} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	// 相对路径排序：00-cd1/ 子目录在前，其后是同级文件
	write(filepath.Join(dir, "00-cd1", "01-正文.mp3"), "mp3-bytes-2")
	write(filepath.Join(dir, "01-开场.mp3"), "mp3-bytes")
	write(filepath.Join(dir, "02-远端.strm"), "https://cdn.example.com/a.mp3\n")
	localTarget := filepath.Join(shared, "尾巴.flac")
	write(localTarget, "flac-bytes")
	write(filepath.Join(dir, "03-本地.strm"), localTarget+"\n")
	// 非音频文件应被忽略
	write(filepath.Join(dir, "cover.jpg"), "jpg")
	write(filepath.Join(dir, "readme.txt"), "txt")

	book, err := svc.ImportLocalAudioDir(ctx, "u1", dir)
	if err != nil {
		t.Fatalf("导入有声书目录失败: %v", err)
	}
	if book.Type != 1 {
		t.Fatalf("Type = %d，期望 1（音频）", book.Type)
	}
	if !book.LocalExternal || book.LocalPath != dir {
		t.Fatalf("应原地引用目录，得到 LocalPath=%q external=%v", book.LocalPath, book.LocalExternal)
	}
	if book.Name != "有声书" {
		t.Fatalf("书名应取目录名，得到 %q", book.Name)
	}
	if book.TotalChapterNum != 4 {
		t.Fatalf("章节数 = %d，期望 4（忽略 cover.jpg / readme.txt）", book.TotalChapterNum)
	}

	chapters, err := svc.ListChapters(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantTitles := []string{"01-正文", "01-开场", "02-远端", "03-本地"}
	for i, want := range wantTitles {
		if chapters[i].Title != want {
			t.Fatalf("第 %d 章标题 = %q，期望 %q（完整目录 %+v）", i, chapters[i].Title, want, chapters)
		}
	}

	// 本地音频文件 → 本地音频流端点
	first, err := svc.LocalChapterContent(ctx, "u1", book.ID, 1)
	if err != nil {
		t.Fatalf("读第 2 章失败: %v", err)
	}
	if first.Type != "audio" || len(first.Tracks) != 1 {
		t.Fatalf("第 2 章应为单轨音频: %+v", first)
	}
	if !strings.HasPrefix(first.Tracks[0], "/api/reader/local/audio?") {
		t.Fatalf("本地音频应走本地流端点: %q", first.Tracks[0])
	}

	// .strm 指向远端 → 媒体代理
	remote, err := svc.LocalChapterContent(ctx, "u1", book.ID, 2)
	if err != nil {
		t.Fatalf("读第 3 章失败: %v", err)
	}
	if !strings.HasPrefix(remote.Tracks[0], "/api/reader/media?") {
		t.Fatalf("远端 .strm 应走媒体代理: %q", remote.Tracks[0])
	}

	// .strm 指向本地文件 → 本地音频流端点
	local, err := svc.LocalChapterContent(ctx, "u1", book.ID, 3)
	if err != nil {
		t.Fatalf("读第 4 章失败: %v", err)
	}
	if !strings.HasPrefix(local.Tracks[0], "/api/reader/local/audio?") {
		t.Fatalf("本地 .strm 应走本地流端点: %q", local.Tracks[0])
	}
	u, err := url.Parse(local.Tracks[0])
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := svc.VerifyLocalAudioURL(u.Query().Get("b"), u.Query().Get("p"), u.Query().Get("s"))
	if err != nil {
		t.Fatalf("音频签名校验失败: %v", err)
	}
	if resolved != localTarget {
		t.Fatalf("解析出的音频路径 = %q，期望 %q", resolved, localTarget)
	}
}

// TestImportLocalAudioDirNoAudio 目录里没有音频时应给出明确错误。
func TestImportLocalAudioDirNoAudio(t *testing.T) {
	svc := newLocalBookService(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportLocalAudioDir(t.Context(), "u1", dir); err == nil {
		t.Fatal("没有音频文件时应报错")
	}
}

// TestLocalAudioURLSignature 音频流地址的签名可校验、改一处即失效。
func TestLocalAudioURLSignature(t *testing.T) {
	svc := newLocalBookService(t)
	path := filepath.Join("media", "有声书", "01.mp3")

	raw := svc.LocalAudioURL("book-1", path)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "/api/reader/local/audio?b=book-1&p=") {
		t.Fatalf("音频地址格式不对: %q", raw)
	}
	got, err := svc.VerifyLocalAudioURL("book-1", u.Query().Get("p"), u.Query().Get("s"))
	if err != nil || got != path {
		t.Fatalf("签名校验失败: %v / %q", err, got)
	}
	if _, err := svc.VerifyLocalAudioURL("book-1", u.Query().Get("p"), "deadbeef"); err == nil {
		t.Fatal("错误签名应校验失败")
	}
	if _, err := svc.VerifyLocalAudioURL("book-2", u.Query().Get("p"), u.Query().Get("s")); err == nil {
		t.Fatal("换一本书后签名应失效")
	}
}

// TestIsRemoteMediaURL 区分远端地址与本地路径。
func TestIsRemoteMediaURL(t *testing.T) {
	remote := []string{
		"https://cdn.example.com/a.mp3",
		"http://x/y.flac",
		"webdav://host/a.mp3",
		"/api/strm/play/local/abc",
	}
	for _, raw := range remote {
		if !isRemoteMediaURL(raw) {
			t.Errorf("%q 应判为远端", raw)
		}
	}
	local := []string{
		"本地音频.flac",
		`D:\media\有声书\01.mp3`,
		"sub/02.mp3",
	}
	for _, raw := range local {
		if isRemoteMediaURL(raw) {
			t.Errorf("%q 应判为本地路径", raw)
		}
	}
}

// TestImportEPUBWithoutCover 没有封面图时不应硬凑，CoverURL 保持为空。
func TestImportEPUBWithoutCover(t *testing.T) {
	svc := newLocalBookService(t)
	book, err := svc.ImportLocalBook(t.Context(), "u1", "无封面.epub", epubFixture(t))
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if book.CoverURL != "" {
		t.Fatalf("无封面书籍不该有 cover_url: %q", book.CoverURL)
	}
}

// TestImportEPUBCoverFallbackOnBrokenDeclaration 声明的封面 id 指不到条目时，
// 应退回“文件名像封面”的图片，而不是直接没有封面。
func TestImportEPUBCoverFallbackOnBrokenDeclaration(t *testing.T) {
	svc := newLocalBookService(t)
	coverBytes := []byte("\xff\xd8\xff\xe0fallbackjpeg")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("mimetype", "application/epub+zip")
	add("META-INF/container.xml", `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`)
	// meta 指向一个 manifest 里不存在的 id
	add("OEBPS/content.opf", `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata><meta name="cover" content="not-exist"/></metadata>
  <manifest>
    <item id="c1" href="ch1.xhtml" media-type="application/xhtml+xml"/>
    <item id="pic" href="images/封面.png" media-type="image/png"/>
  </manifest>
  <spine><itemref idref="c1"/></spine>
</package>`)
	add("OEBPS/ch1.xhtml", `<html xmlns="http://www.w3.org/1999/xhtml"><body><p>内容。</p></body></html>`)
	// 声明失效，只能靠文件名（含“封面”）兜底
	add("OEBPS/images/封面.png", string(coverBytes))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	book, err := svc.ImportLocalBook(t.Context(), "u1", "兜底.epub", buf.Bytes())
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if !strings.HasPrefix(book.CoverURL, "/api/reader/local/asset?") {
		t.Fatalf("应兜底找到封面: %q", book.CoverURL)
	}
	u, err := url.Parse(book.CoverURL)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := svc.VerifyLocalAssetURL(u.Query().Get("b"), u.Query().Get("p"), u.Query().Get("s"))
	if err != nil {
		t.Fatalf("封面签名校验失败: %v", err)
	}
	if filepath.Base(entry) != "封面.png" {
		t.Fatalf("兜底应命中封面.png，实际 %q", entry)
	}
	data, _, err := svc.ReadLocalAsset(t.Context(), book.ID, entry)
	if err != nil || !bytes.Equal(data, coverBytes) {
		t.Fatalf("读兜底封面失败: %v / %q", err, data)
	}
}
