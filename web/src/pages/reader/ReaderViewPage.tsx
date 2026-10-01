import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useParams } from 'react-router-dom'
import toast from 'react-hot-toast'
import {
  ArrowLeft,
  BookOpen,
  LayoutList,
  ListEnd,
  Loader2,
  Minus,
  Moon,
  Plus,
  ScrollText,
  Sun,
} from 'lucide-react'
import { Virtuoso } from 'react-virtuoso'

import { readerAPI, type ReaderBook, type ReaderChapter, type ReaderChapterContent } from '../../api/reader'
import { READER_THEMES, getReaderTheme, useReaderSettingsStore } from '../../stores/readerSettings'
import { ReaderAudioPanel } from './ReaderAudioPanel'
import { ReaderComic } from './ReaderComic'

// 文本阅读器（仿 legado ReadBookActivity：主题配色、点击区域、上下章、
// 进度记忆、翻页/滚动双模式；桌面端限宽居中，支持键盘翻页）。

const COLUMN_GAP = 48

/** 正文里的图片占位行前缀（本地 EPUB 的图片，服务端已换成签名地址）。 */
const IMG_MARK = '[img]'

/** 菜单打开时正文下移过渡（与顶栏动画同节奏）。 */
const MENU_SHIFT = 'transition-transform duration-200'

// 滚轮翻页参数（deltaY 已按 deltaMode 归一化成像素）
/** 单次 deltaY 达到这个量视为鼠标滚轮的一格（一格一页）。 */
const WHEEL_NOTCH = 40
/** 触控板小步长累计到这个量翻一页。 */
const WHEEL_SWIPE_THRESHOLD = 60
/** 两次翻页的最小间隔，与 220ms 翻页动画对齐。 */
const WHEEL_TURN_COOLDOWN = 220
/** 滚轮事件间隔超过这个毫秒数算新手势，重新累计（用于判断触控板一次滑动结束）。 */
const WHEEL_GESTURE_GAP = 180

function firstReadableIndex(chapters: ReaderChapter[]): number {
  const i = chapters.findIndex((c) => !c.is_volume && c.url)
  if (i !== -1) return i
  // 本地导入的章节没有 url，退回第一个非卷名章
  const j = chapters.findIndex((c) => !c.is_volume)
  return j === -1 ? 0 : j
}

export default function ReaderViewPage() {
  const { bookId = '' } = useParams()
  const settings = useReaderSettingsStore()
  const theme = getReaderTheme(settings.themeId, settings.night)

  const [book, setBook] = useState<ReaderBook | null>(null)
  const [chapters, setChapters] = useState<ReaderChapter[]>([])
  const [chapterIndex, setChapterIndex] = useState<number | null>(null)
  const [content, setContent] = useState<string | null>(null)
  const [contentType, setContentType] = useState<'text' | 'audio' | 'image'>('text')
  const [loadingStage, setLoadingStage] = useState<'book' | 'content' | null>('book')
  const [error, setError] = useState('')

  const [menuOpen, setMenuOpen] = useState(false)
  const [panel, setPanel] = useState<'none' | 'toc' | 'style'>('none')
  // 顶栏高度：菜单打开时正文整体下移这么多，顶栏就不会压住开头几行
  const topBarRef = useRef<HTMLDivElement>(null)
  const [menuInset, setMenuInset] = useState(0)
  // 滚轮翻页的累计量 / 冷却 / 一次手势只翻一页的锁
  const readerRef = useRef<HTMLDivElement>(null)
  const wheelAccumRef = useRef(0)
  const wheelLastEventRef = useRef(0)
  const wheelLastTurnRef = useRef(0)
  const wheelSwipeLockedRef = useRef(false)

  // 分页状态
  const viewportRef = useRef<HTMLDivElement>(null)
  const scrollRef = useRef<HTMLDivElement>(null)
  const contentRef = useRef<HTMLDivElement>(null)
  const [page, setPage] = useState(0)
  const [pageCount, setPageCount] = useState(1)
  const [vw, setVw] = useState(0)
  // 音频/漫画媒体状态
  const [media, setMedia] = useState<ReaderChapterContent | null>(null)
  const [restorePos, setRestorePos] = useState(0) // 音频秒数 / 漫画图片序号
  const [comicPage, setComicPage] = useState(0)
  const [currentImage, setCurrentImage] = useState(0)
  const [scrollToImage, setScrollToImage] = useState<number | null>(null)
  const lastMediaSaveRef = useRef(0)
  const contentCache = useRef(new Map<string, ReaderChapterContent>())
  const pendingPosRef = useRef(0)
  const pendingEndRef = useRef(false)

  // ── 加载书籍与章节 ──
  useEffect(() => {
    let cancelled = false
    ;(async () => {
      try {
        setLoadingStage('book')
        setError('')
        const books = await readerAPI.listBooks()
        const b = books.find((x) => x.id === bookId)
        if (!b) throw new Error('书籍不存在或已移出书架')
        if (cancelled) return
        setBook(b)
        let chs = await readerAPI.listChapters(b.id)
        if (chs.length === 0) {
          if (b.is_local) throw new Error('本地书籍目录为空，请重新导入该书')
          const toc = await readerAPI.toc({
            source_url: b.origin,
            book_url: b.book_url,
            toc_url: b.toc_url || b.book_url,
          })
          if (toc.length === 0) throw new Error('目录为空，尝试到详情页刷新目录')
          const toSave = toc.map((c) => ({ index: c.index, title: c.title, url: c.url, is_volume: c.is_volume }))
          await readerAPI.saveChapters(b.id, toSave).catch(() => undefined)
          chs = toSave
        }
        if (cancelled) return
        setChapters(chs)
        // URL 上带 chapter 才用它；没有这个参数就不能当成 0，否则每次进来都回第一章
        const qChapterRaw = new URLSearchParams(window.location.search).get('chapter')
        const qChapter = qChapterRaw && qChapterRaw.trim() !== '' ? Number(qChapterRaw) : NaN
        let idx = firstReadableIndex(chs)
        if (Number.isInteger(qChapter) && chs[qChapter] && !chs[qChapter].is_volume) idx = qChapter
        else if (b.dur_chapter_index > 0 && chs[b.dur_chapter_index] && !chs[b.dur_chapter_index].is_volume) {
          idx = b.dur_chapter_index
        }
        pendingPosRef.current = b.dur_chapter_pos ?? 0
        setChapterIndex(idx)
      } catch (e) {
        if (!cancelled) setError((e as Error).message || '加载失败')
      } finally {
        if (!cancelled) setLoadingStage(null)
      }
    })()
    return () => {
      cancelled = true
      contentCache.current.clear()
    }
  }, [bookId])

  // ── 加载章节正文（带缓存与下一章预取） ──
  useEffect(() => {
    if (chapterIndex === null || !book || chapters.length === 0) return
    const ch = chapters[chapterIndex]
    if (!ch) return
    let cancelled = false
    ;(async () => {
      setContent(null)
      setLoadingStage('content')
      try {
        const cacheKey = String(chapterIndex)
        let ct = contentCache.current.get(cacheKey)
        if (!ct) {
          ct = await readerAPI.bookContent(book.id, chapterIndex)
          contentCache.current.set(cacheKey, ct)
        }
        if (cancelled) return
        setContentType(ct.type)
        setMedia(ct)
        setContent(ct.content ?? '')
        setPage(0)
        setComicPage(0)
        setCurrentImage(0)
        // 音频/漫画的进度恢复值在这里取走（文本由排版/滚动效果消费 pendingPosRef）
        if (ct.type !== 'text') {
          setRestorePos(pendingPosRef.current)
          pendingPosRef.current = 0
        }
        // 进度上报（pos 保留原值，排版完成后才被消费清零）
        readerAPI
          .saveProgress(book.id, { chapter_index: chapterIndex, pos: pendingPosRef.current, chapter_title: ch.title })
          .catch(() => undefined)
        // 预取下一章
        if (!contentCache.current.has(String(chapterIndex + 1))) {
          readerAPI
            .bookContent(book.id, chapterIndex + 1)
            .then((c) => contentCache.current.set(String(chapterIndex + 1), c))
            .catch(() => undefined)
        }
      } catch (e) {
        if (!cancelled) {
          const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error ?? (e as Error).message
          setError(msg || '正文加载失败')
        }
      } finally {
        if (!cancelled) setLoadingStage(null)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [chapterIndex, book, chapters])

  // ── 分页排版（CSS 多栏 + 平移） ──
  const relayout = useCallback(() => {
    const vp = viewportRef.current
    if (!vp) return
    const width = vp.clientWidth
    setVw(width)
    const total = vp.scrollWidth
    const count = Math.max(1, Math.ceil((total + COLUMN_GAP) / (width + COLUMN_GAP)))
    setPageCount(count)
    setPage((p) => {
      if (pendingEndRef.current && count > 0) {
        pendingEndRef.current = false
        return count - 1
      }
      if (pendingPosRef.current > 0) {
        const pos = Math.min(pendingPosRef.current, count - 1)
        pendingPosRef.current = 0
        return pos
      }
      return Math.min(p, count - 1)
    })
  }, [])

  useLayoutEffect(() => {
    relayout()
  }, [relayout, content, settings.fontSize, settings.lineHeight, settings.paragraphSpacing, settings.pageMode, vw])

  useEffect(() => {
    const vp = viewportRef.current
    if (!vp) return
    const ro = new ResizeObserver(() => relayout())
    ro.observe(vp)
    return () => ro.disconnect()
  }, [relayout, content, settings.pageMode])

  // 滚动模式恢复进度
  useEffect(() => {
    if (settings.pageMode !== 'scroll' || content === null) return
    const el = scrollRef.current
    if (el && pendingPosRef.current > 0) {
      el.scrollTop = pendingPosRef.current
      pendingPosRef.current = 0
    }
  }, [content, settings.pageMode])

  // ── 进度保存（翻页 / 滚动） ──
  const savePos = useCallback(
    (pos: number) => {
      if (!book || chapterIndex === null) return
      const ch = chapters[chapterIndex]
      readerAPI
        .saveProgress(book.id, { chapter_index: chapterIndex, pos, chapter_title: ch?.title ?? '' })
        .catch(() => undefined)
    },
    [book, chapterIndex, chapters],
  )

  useEffect(() => {
    if (settings.pageMode !== 'page' || content === null || chapterIndex === null) return
    const t = setTimeout(() => savePos(page), 1500)
    return () => clearTimeout(t)
  }, [page, content, chapterIndex, settings.pageMode, savePos])

  // 漫画单页进度保存
  useEffect(() => {
    if (contentType !== 'image' || settings.pageMode !== 'page' || !media || chapterIndex === null) return
    const t = setTimeout(() => savePos(comicPage), 1200)
    return () => clearTimeout(t)
  }, [comicPage, contentType, settings.pageMode, media, chapterIndex, savePos])

  // 音频/漫画滚动：节流进度保存
  const throttledMediaSave = useCallback(
    (pos: number) => {
      if (!book || chapterIndex === null) return
      const now = Date.now()
      if (now - lastMediaSaveRef.current < (contentType === 'audio' ? 10_000 : 2_000)) return
      lastMediaSaveRef.current = now
      savePos(pos)
    },
    [book, chapterIndex, contentType, savePos],
  )

  // 漫画：翻到章尾/恢复进度定位
  useEffect(() => {
    if (!media || media.type !== 'image') return
    const n = media.images?.length ?? 0
    if (pendingEndRef.current && n > 0) {
      setComicPage(n - 1)
      pendingEndRef.current = false
      return
    }
    if (restorePos > 0) {
      setComicPage(Math.min(restorePos, Math.max(0, n - 1)))
    }
  }, [media, restorePos])

  // ── 章节导航 ──
  const goChapter = useCallback(
    (delta: number, atEnd = false) => {
      if (chapterIndex === null) return
      let i = chapterIndex + delta
      while (i >= 0 && i < chapters.length && chapters[i].is_volume) i += delta
      if (i < 0 || i >= chapters.length) {
        toast(delta < 0 ? '已经是第一章' : '已经是最后一章')
        return
      }
      pendingEndRef.current = atEnd
      setError('')
      setChapterIndex(i)
    },
    [chapterIndex, chapters],
  )

  // 听书：进度条/章节列表跳章（不沿用 goChapter 的越界提示，直接落位）
  const jumpToChapter = useCallback((idx: number) => {
    if (idx < 0 || idx >= chapters.length) return
    pendingEndRef.current = false
    setError('')
    setChapterIndex(idx)
  }, [chapters.length])

  // 听书：是否还有下一章（片尾跳过/播完时决定续播还是停住）
  const hasNextAudioChapter = useMemo(() => {
    if (chapterIndex === null) return false
    for (let i = chapterIndex + 1; i < chapters.length; i++) {
      if (!chapters[i].is_volume) return true
    }
    return false
  }, [chapterIndex, chapters])

  // 听书：片头/片尾跳过秒数按书写入（对应 legado Book.openCredits/closeCredits）
  const saveAudioCredits = useCallback(
    (open: number, close: number) => {
      if (!book) return
      setBook((prev) => (prev ? { ...prev, open_credits: open, close_credits: close } : prev))
      readerAPI.saveAudioConfig(book.id, { open_credits: open, close_credits: close }).catch(() => undefined)
    },
    [book],
  )

  const goPrev = useCallback(() => {
    if (contentType === 'audio') {
      goChapter(-1)
      return
    }
    if (contentType === 'image' && settings.pageMode === 'page') {
      if (comicPage > 0) setComicPage((p) => p - 1)
      else goChapter(-1, true)
      return
    }
    if (settings.pageMode === 'scroll') {
      scrollRef.current?.scrollBy({ top: -window.innerHeight * 0.9, behavior: 'auto' })
      return
    }
    if (page > 0) setPage((p) => p - 1)
    else goChapter(-1, true)
  }, [contentType, settings.pageMode, comicPage, page, goChapter])

  const goNext = useCallback(() => {
    if (contentType === 'audio') {
      goChapter(1)
      return
    }
    if (contentType === 'image' && settings.pageMode === 'page') {
      const n = media?.images?.length ?? 0
      if (comicPage < n - 1) setComicPage((p) => p + 1)
      else goChapter(1)
      return
    }
    if (settings.pageMode === 'scroll') {
      const el = scrollRef.current
      if (el && el.scrollTop + el.clientHeight >= el.scrollHeight - 2) goChapter(1)
      else el?.scrollBy({ top: window.innerHeight * 0.9, behavior: 'auto' })
      return
    }
    if (page < pageCount - 1) setPage((p) => p + 1)
    else goChapter(1)
  }, [contentType, settings.pageMode, media, comicPage, page, pageCount, goChapter])

  // ── 键盘（桌面端） ──
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        if (panel !== 'none') setPanel('none')
        else if (menuOpen) setMenuOpen(false)
        else setMenuOpen(true)
        return
      }
      if (e.key === 'ArrowLeft' || e.key === 'PageUp') goPrev()
      else if (e.key === 'ArrowRight' || e.key === 'PageDown' || e.key === ' ') {
        e.preventDefault()
        goNext()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [goPrev, goNext, menuOpen, panel])

  // ── 鼠标滚轮翻页（仅翻页模式） ──
  // 往上滚=上一页，往下滚=下一页。滚动模式保持浏览器原生滚动，不做接管。
  // 滚轮必须用原生监听器：React 的 onWheel 是 passive 的，调不了 preventDefault。
  useEffect(() => {
    const el = readerRef.current
    if (!el) return
    // 滚动模式/听书面板/漫画滚动交给浏览器自己处理；菜单打开时不翻页
    if (settings.pageMode !== 'page' || menuOpen) return
    if (contentType !== 'text' && contentType !== 'image') return

    const turn = (forward: boolean) => (forward ? goNext() : goPrev())
    const onWheel = (e: WheelEvent) => {
      // ctrl+滚轮是浏览器缩放，别抢
      if (e.ctrlKey || e.metaKey || e.deltaY === 0) return
      e.preventDefault()
      const now = Date.now()
      // deltaMode: 0=像素 1=行 2=页，统一折算成像素
      const unit = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? 100 : 1
      const delta = e.deltaY * unit
      if (now - wheelLastEventRef.current > WHEEL_GESTURE_GAP) {
        wheelAccumRef.current = 0
        wheelSwipeLockedRef.current = false
      }
      wheelLastEventRef.current = now

      if (Math.abs(delta) >= WHEEL_NOTCH) {
        // 鼠标滚轮：一格一页，但至少隔一次翻页动画的时间
        if (now - wheelLastTurnRef.current < WHEEL_TURN_COOLDOWN) return
        wheelLastTurnRef.current = now
        wheelAccumRef.current = 0
        turn(delta > 0)
        return
      }
      // 触控板：小步长累计到阈值再翻，一次手势只翻一页，避免惯性连翻
      if (wheelSwipeLockedRef.current) return
      wheelAccumRef.current += delta
      if (Math.abs(wheelAccumRef.current) < WHEEL_SWIPE_THRESHOLD) return
      const forward = wheelAccumRef.current > 0
      wheelAccumRef.current = 0
      wheelSwipeLockedRef.current = true
      wheelLastTurnRef.current = now
      turn(forward)
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [settings.pageMode, menuOpen, contentType, goPrev, goNext])

  const currentChapter = chapterIndex !== null ? chapters[chapterIndex] : null

  // 顶栏高度量一次：菜单打开时正文下移，开头几行不被顶栏压住。
  // 用 transform 而不是 padding，避免改变视口高度触发重新分页。
  const menuShiftStyle = useMemo(
    () => (menuOpen && menuInset > 0 ? { transform: `translateY(${menuInset}px)` } : undefined),
    [menuOpen, menuInset],
  )
  useLayoutEffect(() => {
    if (!menuOpen) {
      setMenuInset(0)
      return
    }
    const h = topBarRef.current?.offsetHeight ?? 0
    setMenuInset((cur) => (cur === h ? cur : h))
  }, [menuOpen])

  // 菜单进度条按内容类型适配：文本=页/滚动位置，音频=章节，漫画=图片序号
  const imageCount = media?.images?.length ?? 0
  const sliderCfg = (() => {
    if (contentType === 'audio') {
      return {
        min: 0,
        max: Math.max(0, chapters.length - 1),
        value: Math.max(0, chapterIndex ?? 0),
        onChange: (v: number) => jumpToChapter(v),
      }
    }
    if (contentType === 'image') {
      const max = Math.max(0, imageCount - 1)
      if (settings.pageMode === 'page') {
        return { min: 0, max, value: Math.min(comicPage, max), onChange: (v: number) => setComicPage(v) }
      }
      return { min: 0, max, value: Math.min(currentImage, max), onChange: (v: number) => setScrollToImage(v) }
    }
    return {
      min: 1,
      max: Math.max(1, settings.pageMode === 'page' ? pageCount : 1000),
      value:
        settings.pageMode === 'page'
          ? page + 1
          : Math.round(
              ((scrollRef.current?.scrollTop ?? 0) /
                Math.max(1, (scrollRef.current?.scrollHeight ?? 1) - (scrollRef.current?.clientHeight ?? 1))) *
                1000,
            ),
      onChange: (v: number) => {
        if (settings.pageMode === 'page') setPage(v - 1)
        else {
          const el = scrollRef.current
          if (el) el.scrollTop = (v / 1000) * (el.scrollHeight - el.clientHeight)
        }
      },
    }
  })()
  const paragraphs = (content ?? '').split('\n').map((p) => p.trim()).filter(Boolean)

  // 正文段落：普通段落按缩进排版，[img] 行渲染成居中图片
  const renderParagraph = (line: string, key: number) => {
    if (line.startsWith(IMG_MARK)) {
      return (
        <p key={key} style={{ marginBottom: settings.paragraphSpacing, textAlign: 'center' }}>
          <img
            src={line.slice(IMG_MARK.length)}
            alt=""
            referrerPolicy="no-referrer"
            style={{ maxWidth: '100%', maxHeight: '70vh', margin: '0 auto', objectFit: 'contain' }}
          />
        </p>
      )
    }
    return (
      <p key={key} style={{ textIndent: '2em', marginBottom: settings.paragraphSpacing }}>
        {line}
      </p>
    )
  }

  // ── 渲染 ──
  if (error && !book) {
    return (
      <div className="flex min-h-[100dvh] flex-col items-center justify-center gap-4 bg-[var(--app-bg)] text-[var(--app-text)]">
        <p className="text-sm text-[var(--app-muted)]">{error}</p>
        <button type="button" onClick={() => window.history.back()} className="btn-outline text-xs">
          返回
        </button>
      </div>
    )
  }

  return (
    <div ref={readerRef} className="fixed inset-0 z-40 flex flex-col" style={{ backgroundColor: theme.bg, color: theme.text }}>
      {/* 正文视口 */}
      <div className="relative flex-1 overflow-hidden">
        <div className="mx-auto h-full w-full max-w-[900px]">
          {contentType === 'audio' ? (
            media && media.tracks && media.tracks.length > 0 ? (
              <ReaderAudioPanel
                src={media.tracks[0]}
                title={currentChapter?.title ?? book?.name ?? '播放'}
                cover={book?.cover_url ?? ''}
                theme={theme}
                initialPos={restorePos}
                openCredits={book?.open_credits ?? 0}
                closeCredits={book?.close_credits ?? 0}
                chapters={chapters}
                chapterIndex={chapterIndex}
                hasNext={hasNextAudioChapter}
                transcoding={media.transcoding ?? false}
                onProgress={throttledMediaSave}
                onPrevChapter={() => goChapter(-1)}
                onNextChapter={() => goChapter(1)}
                onSelectChapter={jumpToChapter}
                onEnded={() => goChapter(1)}
                onCreditsChange={saveAudioCredits}
                onToggleMenu={() => setMenuOpen((v) => !v)}
              />
            ) : (
              <div className="flex h-full items-center justify-center text-sm opacity-60" style={{ color: theme.text }}>
                {loadingStage !== null ? <Loader2 className="animate-spin opacity-60" size={24} /> : '本章没有可播放的音频'}
              </div>
            )
          ) : contentType === 'image' ? (
            <ReaderComic
              images={media?.images ?? []}
              theme={theme}
              mode={settings.pageMode}
              page={comicPage}
              onZone={(zone) => {
                if (zone === 'center') setMenuOpen((v) => !v)
              }}
              initialImage={restorePos}
              onProgress={(idx) => {
                setCurrentImage(idx)
                throttledMediaSave(idx)
              }}
              scrollTo={scrollToImage}
              onScrolled={() => setScrollToImage(null)}
            />
          ) : settings.pageMode === 'page' ? (
            /* 左右/上下留边（legado 默认左右16/上下6），避免正文贴屏幕边；
               菜单打开时整体下移一个顶栏高度，顶栏不再压住正文（用 transform，
               不改高度也就不触发重新分页） */
            <div className={`h-full px-4 py-2 ${MENU_SHIFT}`} style={menuShiftStyle}>
              <div ref={viewportRef} className="relative h-full overflow-hidden">
                <div
                  ref={contentRef}
                  className="h-full"
                  style={{
                    columnWidth: `${Math.max(vw, 1)}px`,
                    columnGap: `${COLUMN_GAP}px`,
                    columnFill: 'auto',
                    transform: `translateX(-${page * (vw + COLUMN_GAP)}px)`,
                    transition: 'transform 220ms ease',
                    fontSize: settings.fontSize,
                    lineHeight: settings.lineHeight,
                  }}
                >
                  {paragraphs.map((p, i) => renderParagraph(p, i))}
                </div>
              </div>
            </div>
          ) : (
            <div
              ref={scrollRef}
              onScroll={(e) => {
                const el = e.currentTarget
                if (chapterIndex === null) return
                const max = el.scrollHeight - el.clientHeight
                if (max > 0) {
                  const pct = el.scrollTop / max
                  if (Math.abs(pct * 1000 - (Number(el.dataset.last) ?? -1) * 1000) > 20) {
                    el.dataset.last = String(pct)
                    savePos(Math.round(el.scrollTop))
                  }
                }
              }}
              className={`h-full overflow-y-auto px-4 ${MENU_SHIFT}`}
              style={{ fontSize: settings.fontSize, lineHeight: settings.lineHeight, ...menuShiftStyle }}
            >
              <div className="py-4">
                {paragraphs.map((p, i) => renderParagraph(p, i))}
              </div>
            </div>
          )}

          {/* 加载 / 错误 / 空内容态 */}
          {(loadingStage !== null || (content !== null && paragraphs.length === 0)) && contentType === 'text' && (
            <div className="pointer-events-none absolute inset-0 flex items-center justify-center" style={{ color: theme.text }}>
              {loadingStage !== null ? (
                <Loader2 className="animate-spin opacity-60" size={24} />
              ) : (
                <p className="text-sm opacity-60">本章内容为空</p>
              )}
            </div>
          )}
          {error && (
            <div className="absolute inset-0 flex flex-col items-center justify-center gap-3" style={{ color: theme.text }}>
              <p className="text-sm opacity-80">{error}</p>
              <button
                type="button"
                onClick={() => {
                  setError('')
                  if (chapterIndex !== null) {
                    contentCache.current.delete(String(chapterIndex))
                    setChapterIndex(chapterIndex)
                  }
                }}
                className="rounded-xl border px-4 py-1.5 text-xs font-bold"
                style={{ borderColor: theme.text }}
              >
                重试
              </button>
            </div>
          )}
        </div>

        {(contentType === 'text' || (contentType === 'image' && settings.pageMode === 'page')) && (
          <div className="absolute inset-0 grid grid-cols-[30%_40%_30%]">
            <button type="button" aria-label="上一页" onClick={goPrev} className="cursor-w-resize" />
            <button
              type="button"
              aria-label="菜单"
              onClick={() => setMenuOpen((v) => !v)}
              className="cursor-default"
            />
            <button type="button" aria-label="下一页" onClick={goNext} className="cursor-e-resize" />
          </div>
        )}
      </div>

      {/* 页脚页码（翻页模式） */}
      {settings.pageMode === 'page' && contentType === 'text' && content !== null && (
        <div className="pointer-events-none pb-2 text-center text-2xs opacity-50" style={{ color: theme.text }}>
          {page + 1} / {pageCount} · {currentChapter?.title ?? ''}
        </div>
      )}

      {/* 主菜单（仿 legado ReadMenu） */}
      {menuOpen && (
        <>
          <button
            type="button"
            aria-label="关闭菜单"
            className="fixed inset-0 z-40 cursor-default bg-black/30"
            onClick={() => setMenuOpen(false)}
          />
          {/* 顶栏 */}
          <div
            ref={topBarRef}
            className="fixed inset-x-0 top-0 z-50 flex items-center gap-3 border-b px-4 py-3 backdrop-blur"
            style={{ backgroundColor: theme.bg, borderColor: theme.text + '22' }}
          >
            <button
              type="button"
              onClick={() => window.history.back()}
              className="rounded-xl p-1.5 opacity-70 hover:opacity-100"
              style={{ color: theme.text }}
            >
              <ArrowLeft size={18} />
            </button>
            <div className="min-w-0 flex-1 text-center">
              <p className="truncate text-sm font-bold">{book?.name ?? '阅读'}</p>
              <p className="truncate text-2xs opacity-60">{currentChapter?.title ?? ''}</p>
            </div>
            <div className="w-9" />
          </div>

          {/* 底部菜单 */}
          <div
            className="fixed inset-x-0 bottom-0 z-50 space-y-3 rounded-t-2xl border-t px-4 pb-6 pt-4 backdrop-blur"
            style={{ backgroundColor: theme.bg, borderColor: theme.text + '22' }}
          >
            {/* 章节行 */}
            <div className="flex items-center gap-3" style={{ color: theme.text }}>
              <button
                type="button"
                onClick={() => goChapter(-1)}
                className="rounded-xl px-2 py-1 text-xs font-bold opacity-80 hover:opacity-100"
              >
                上一章
              </button>
              <input
                type="range"
                min={sliderCfg.min}
                max={sliderCfg.max}
                value={sliderCfg.value}
                onChange={(e) => sliderCfg.onChange(Number(e.target.value))}
                className="flex-1 accent-current"
                style={{ accentColor: theme.accent }}
              />
              <button
                type="button"
                onClick={() => goChapter(1)}
                className="rounded-xl px-2 py-1 text-xs font-bold opacity-80 hover:opacity-100"
              >
                下一章
              </button>
            </div>

            {/* 动作行（目录 / 界面 / 夜间 / 模式） */}
            <div className="grid grid-cols-4 pt-1" style={{ color: theme.text }}>
              {([
                { icon: <LayoutList size={18} />, label: '目录', action: () => setPanel(panel === 'toc' ? 'none' : 'toc') },
                { icon: <BookOpen size={18} />, label: '界面', action: () => setPanel(panel === 'style' ? 'none' : 'style') },
                {
                  icon: settings.night ? <Sun size={18} /> : <Moon size={18} />,
                  label: settings.night ? '日间' : '夜间',
                  action: () => settings.toggleNight(),
                },
                {
                  icon: settings.pageMode === 'page' ? <ScrollText size={18} /> : <ListEnd size={18} />,
                  label: settings.pageMode === 'page' ? '滚动' : '翻页',
                  action: () => settings.setPageMode(settings.pageMode === 'page' ? 'scroll' : 'page'),
                },
              ]).map((item) => (
                <button
                  key={item.label}
                  type="button"
                  onClick={item.action}
                  className="flex flex-col items-center gap-1 py-1 opacity-80 hover:opacity-100"
                >
                  {item.icon}
                  <span className="text-2xs">{item.label}</span>
                </button>
              ))}
            </div>

            {/* 界面设置面板（主题 / 字号 / 行距 / 段距） */}
            {panel === 'style' && (
              <div
                className="absolute bottom-full inset-x-0 border-t px-4 py-4"
                style={{ backgroundColor: theme.bg, borderColor: theme.text + '22', color: theme.text }}
              >
                <div className="flex flex-wrap items-center gap-2">
                  {READER_THEMES.map((t) => {
                    const bg = settings.night ? t.nightBg : t.bg
                    const active = settings.themeId === t.id
                    return (
                      <button
                        key={t.id}
                        type="button"
                        onClick={() => settings.setThemeId(t.id)}
                        className={`h-8 w-8 rounded-full border-2 ${active ? 'scale-110' : ''}`}
                        style={{ backgroundColor: bg, borderColor: active ? theme.accent : theme.text + '44' }}
                        title={t.name}
                      />
                    )
                  })}
                </div>
                <div className="mt-4 flex flex-wrap items-center gap-x-6 gap-y-3 text-xs">
                  <div className="flex shrink-0 items-center gap-2">
                    <span className="opacity-70">字号</span>
                    <button type="button" onClick={() => settings.setFontSize(settings.fontSize - 1)} className="rounded-lg border px-2 py-0.5" style={{ borderColor: theme.text + '44' }}>
                      <Minus size={12} />
                    </button>
                    <span className="w-6 text-center font-bold">{settings.fontSize}</span>
                    <button type="button" onClick={() => settings.setFontSize(settings.fontSize + 1)} className="rounded-lg border px-2 py-0.5" style={{ borderColor: theme.text + '44' }}>
                      <Plus size={12} />
                    </button>
                  </div>
                  <div className="flex shrink-0 items-center gap-2">
                    <span className="opacity-70">行距</span>
                    <button type="button" onClick={() => settings.setLineHeight(settings.lineHeight - 0.1)} className="rounded-lg border px-2 py-0.5" style={{ borderColor: theme.text + '44' }}>
                      <Minus size={12} />
                    </button>
                    <span className="w-8 text-center font-bold">{settings.lineHeight.toFixed(1)}</span>
                    <button type="button" onClick={() => settings.setLineHeight(settings.lineHeight + 0.1)} className="rounded-lg border px-2 py-0.5" style={{ borderColor: theme.text + '44' }}>
                      <Plus size={12} />
                    </button>
                  </div>
                  <div className="flex shrink-0 items-center gap-2">
                    <span className="opacity-70">段距</span>
                    <button type="button" onClick={() => settings.setParagraphSpacing(settings.paragraphSpacing - 2)} className="rounded-lg border px-2 py-0.5" style={{ borderColor: theme.text + '44' }}>
                      <Minus size={12} />
                    </button>
                    <span className="w-6 text-center font-bold">{settings.paragraphSpacing}</span>
                    <button type="button" onClick={() => settings.setParagraphSpacing(settings.paragraphSpacing + 2)} className="rounded-lg border px-2 py-0.5" style={{ borderColor: theme.text + '44' }}>
                      <Plus size={12} />
                    </button>
                  </div>
                </div>
              </div>
            )}
          </div>

          {/* 目录：整屏面板。必须放在底部菜单之外——菜单带 backdrop-blur，
              会成为 fixed 后代的包含块，放里面高度会被算成菜单的高度。 */}
          {panel === 'toc' && (
            <div
              className="fixed inset-0 z-[60] flex flex-col"
              style={{ backgroundColor: theme.bg, color: theme.text }}
            >
              <div
                className="flex items-center gap-3 border-b px-4 py-3"
                style={{ borderColor: theme.text + '22' }}
              >
                <button
                  type="button"
                  onClick={() => setPanel('none')}
                  className="rounded-xl p-1.5 opacity-70 hover:opacity-100"
                  aria-label="收起目录"
                >
                  <ArrowLeft size={18} />
                </button>
                <p className="flex-1 truncate text-sm font-bold">
                  {book?.name ?? '目录'}
                  <span className="ml-2 text-2xs font-normal opacity-60">目录（{chapters.length} 章）</span>
                </p>
              </div>
              <div className="min-h-0 flex-1">
                <Virtuoso
                  data={chapters}
                  initialTopMostItemIndex={Math.max(0, chapterIndex ?? 0)}
                  itemContent={(index, ch) => {
                    const isCurrent = index === chapterIndex
                    return (
                      <button
                        type="button"
                        onClick={() => {
                          setPanel('none')
                          setMenuOpen(false)
                          jumpToChapter(index)
                        }}
                        className={`block w-full truncate px-4 py-2.5 text-left text-xs ${
                          ch.is_volume ? 'font-bold opacity-70' : ''
                        }`}
                        style={isCurrent ? { color: theme.accent, fontWeight: 700 } : undefined}
                      >
                        {ch.title}
                      </button>
                    )
                  }}
                />
              </div>
            </div>
          )}
        </>
      )}

    </div>
  )
}
