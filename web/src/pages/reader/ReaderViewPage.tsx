import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useParams } from 'react-router-dom'
import toast from 'react-hot-toast'
import {
  ArrowLeft,
  ArrowLeftRight,
  BookOpen,
  Columns2,
  LayoutList,
  ListEnd,
  Loader2,
  MessageSquare,
  Minus,
  Moon,
  Plus,
  ScrollText,
  Sun,
} from 'lucide-react'
import { Virtuoso, type VirtuosoHandle } from 'react-virtuoso'

import {
  readerAPI,
  type ReaderBook,
  type ReaderBrowserPage,
  type ReaderChapter,
  type ReaderChapterContent,
  type ReaderContentComment,
  type ReaderSearchOrigin,
} from '../../api/reader'
import { useComicSpreads } from '../../hooks/useComicSpreads'
import { useHorizontalSwipe } from '../../hooks/useHorizontalSwipe'
import { useSmoothWheelScroll } from '../../hooks/useSmoothWheelScroll'
import { useReaderAudioStore } from '../../stores/readerAudio'
import { COMIC_IMAGE_FITS, READER_THEMES, getReaderTheme, useReaderSettingsStore } from '../../stores/readerSettings'
import { buildChapterGroups, chapterGroupIndexOf } from '../../utils/chapterGroups'
import BrowserPanel from './BrowserPanel'
import { ReaderAudioPanel } from './ReaderAudioPanel'
import { ReaderComic } from './ReaderComic'
import { SourcePickerDialog } from './SourcePickerDialog'
import { getReaderAudioEngine } from './readerAudioEngine'

// 文本阅读器（仿 legado ReadBookActivity：主题配色、点击区域、上下章、
// 进度记忆、翻页/滚动双模式；桌面端限宽居中，支持键盘翻页）。

const COLUMN_GAP = 48

/** 正文里的图片占位行前缀（本地 EPUB 的图片，服务端已换成签名地址）。 */
const IMG_MARK = '[img]'

/** 听书进度上报节流（毫秒）：按秒记忆，退出最多丢 5 秒。 */
const AUDIO_SAVE_INTERVAL_MS = 5_000
/** 漫画进度上报节流（毫秒）：按图片序号记忆。 */
const COMIC_SAVE_INTERVAL_MS = 2_000

/** 菜单打开时正文下移过渡（与顶栏动画同节奏）。 */
const MENU_SHIFT = 'transition-transform duration-200'

/** 翻页动画时长。正文列的平移过渡，以及跟手拖拽松手后的回弹/滑完，都用这一条。 */
const PAGE_TRANSITION = 'transform 220ms ease'
/** 跟手拖拽要翻页所需的最小位移（px）。 */
const DRAG_TURN_DISTANCE = 45

// 滚轮翻页参数（deltaY 已按 deltaMode 归一化成像素）
/** 单次 deltaY 达到这个量视为鼠标滚轮的一格（一格一页）。 */
const WHEEL_NOTCH = 40
/** 触控板小步长累计到这个量翻一页。 */
const WHEEL_SWIPE_THRESHOLD = 60
/** 两次翻页的最小间隔，与 220ms 翻页动画对齐。 */
const WHEEL_TURN_COOLDOWN = 220
/** 滚轮事件间隔超过这个毫秒数算新手势，重新累计（用于判断触控板一次滑动结束）。 */
const WHEEL_GESTURE_GAP = 180

/**
 * 漫画双页铺开的最小窗口宽度。比这窄时并排两页每页只剩一条竖条，
 * 反而比单页更难看清，所以渲染层按窗口宽度自动退回单页（偏好设置不动）。
 */
const COMIC_SPREAD_MIN_WIDTH = 900

/** 空图片列表的常量引用：避免每次渲染产生新数组、把分组结果的 memo 依赖打散。 */
const NO_IMAGES: string[] = []

/** 一个渲染单元：一段正文文字 + 挂在这一行上的段评气泡。 */
interface ReaderParagraphBlock {
  text: string
  comments: ReaderContentComment[]
}

function firstReadableIndex(chapters: ReaderChapter[]): number {
  const i = chapters.findIndex((c) => !c.is_volume && c.url)
  if (i !== -1) return i
  // 本地导入的章节没有 url，退回第一个非卷名章
  const j = chapters.findIndex((c) => !c.is_volume)
  return j === -1 ? 0 : j
}

/**
 * 漫画跟手拖拽松手后「滑完这一页」。
 *
 * 舞台两侧各摆着相邻的一页，手指拖到哪儿舞台就平移到哪儿。松手要翻页时，
 * 先把新页瞬移到手指离开的位置（这一步必须关掉过渡，否则会先反着滑一段），
 * 读一次布局把这一帧定成过渡起点，再打开过渡滑回 0；与此同时页码 +1/-1，
 * ReaderComic 那边单元格的 left 偏移在同一次提交里跟着换，两者相抵，
 * 屏幕上看到的就是新页从手指离开的位置平滑归位。
 */
function settleComicDrag(el: HTMLDivElement, turn: 1 | -1, dx: number, turnPage: () => void): void {
  const width = el.clientWidth || 1
  el.style.transition = 'none'
  el.style.transform = `translateX(${turn > 0 ? width + dx : -width + dx}px)`
  void el.offsetWidth
  el.style.transition = PAGE_TRANSITION
  el.style.transform = 'translateX(0px)'
  turnPage()
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
  /** 目录面板顶部「区间下拉」选中的组号，随列表滚动同步。 */
  const [tocGroupIndex, setTocGroupIndex] = useState(0)
  // 换源：候选源来自按书名重新搜索的结果；reloadKey 变化时整本书重新加载
  const [switchOpen, setSwitchOpen] = useState(false)
  const [switchLoading, setSwitchLoading] = useState(false)
  const [switchCandidates, setSwitchCandidates] = useState<ReaderSearchOrigin[]>([])
  const [reloadKey, setReloadKey] = useState(0)
  // 正文重载计数：错误态的「重试」只重取本章正文，不重载整本书（换源用 reloadKey）。
  const [contentReloadKey, setContentReloadKey] = useState(0)
  /** 段评承载页：点击段评气泡后由宿主浏览器打开评论页（带书源登录态）。 */
  const [browserPage, setBrowserPage] = useState<ReaderBrowserPage | null>(null)
  // 顶栏高度：菜单打开时正文整体下移这么多，顶栏就不会压住开头几行
  const topBarRef = useRef<HTMLDivElement>(null)
  const [menuInset, setMenuInset] = useState(0)
  // 滚轮翻页的累计量 / 冷却 / 一次手势只翻一页的锁
  const readerRef = useRef<HTMLDivElement>(null)
  const wheelAccumRef = useRef(0)
  const wheelLastEventRef = useRef(0)
  const wheelLastTurnRef = useRef(0)
  const wheelSwipeLockedRef = useRef(false)
  /** 目录面板虚拟列表句柄：区间下拉靠它整段跳转。 */
  const tocRef = useRef<VirtuosoHandle>(null)

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

  // ── 漫画双页铺开（桌面端） ──
  // 窗口宽度决定双页是否可用：偏好在 settings 里（用户可关），但窗口不够宽时
  // 即使开着也退回单页，所以这里额外跟一个窗口宽度。
  const [windowWidth, setWindowWidth] = useState(() => window.innerWidth)
  useEffect(() => {
    const onResize = () => setWindowWidth(window.innerWidth)
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])

  const comicImages = contentType === 'image' && media?.images ? media.images : NO_IMAGES
  const comicDoublePage =
    contentType === 'image' &&
    settings.pageMode === 'page' &&
    settings.comicDoublePage &&
    windowWidth >= COMIC_SPREAD_MIN_WIDTH
  // 分组依赖每张图的原始宽高（横跨两页的宽图要独占一屏），探测窗口跟着当前页走
  const { spreads: comicSpreads, spreadIndexOf: comicSpreadIndexOf } = useComicSpreads(
    comicImages,
    comicDoublePage,
    comicPage,
  )
  /** 当前是第几「屏」。进度仍按图片序号记，屏序号只用于翻页与渲染。 */
  const comicSpreadIndex =
    comicDoublePage && comicSpreads.length > 0
      ? Math.min(comicSpreadIndexOf[comicPage] ?? 0, comicSpreads.length - 1)
      : 0
  /** 当前这一屏是不是并排两页（桌面端双页铺开）。 */
  const comicPaired = comicDoublePage && (comicSpreads[comicSpreadIndex]?.length ?? 0) > 1
  /**
   * 漫画翻页模式且单页显示时接跟手拖拽：舞台左右各摆一页，手指横滑整条舞台跟着走。
   * 并排两页时不接——两侧要摆的是「下一屏」而不是下一张，交给原来的滑动判定即可。
   */
  const comicDragSinglePage = contentType === 'image' && settings.pageMode === 'page' && !comicPaired

  // ── 漫画滚动模式的图片显示尺寸 ──
  // 档位只在上下滚动模式生效：翻页模式是整页缩放进视口，没有「太大/太小」的问题，
  // 掺进来反而会和双页铺开的排版打架。所以翻页模式一律按 default 处理。
  const comicScrollFit = contentType === 'image' && settings.pageMode === 'scroll' ? settings.comicImageFit : 'default'
  /** 图片尺寸档位只在漫画的滚动模式下起作用，别的场景不显示这一行免得点了没反应。 */
  const showComicImageFit = contentType === 'image' && settings.pageMode === 'scroll'
  /**
   * 正文列是否放开 900px 上限。两种情况：漫画双页铺开要吃满窗口宽度；
   * 或者用户把图片尺寸调成了「适应宽度/高度/长边/原图」——这些档位由图片自己定尺寸，
   * 900px 的列会把它们再压回去。
   */
  const comicFullWidth = contentType === 'image' && (comicDoublePage || comicScrollFit !== 'default')

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
        const explicitChapter = Number.isInteger(qChapter) && !!chs[qChapter] && !chs[qChapter].is_volume
        if (explicitChapter) idx = qChapter
        else if (b.dur_chapter_index > 0 && chs[b.dur_chapter_index] && !chs[b.dur_chapter_index].is_volume) {
          idx = b.dur_chapter_index
        }
        // 位置只在「接着上次那一章读」时恢复：URL 显式跳到别的章时，b.dur_chapter_pos
        // 属于另一章，套到新章上会从中间开始（音频表现为一进去就从错的时间点续播）。
        const resumingSavedChapter = !explicitChapter || idx === b.dur_chapter_index
        pendingPosRef.current = resumingSavedChapter ? (b.dur_chapter_pos ?? 0) : 0
        // 听书：引擎是常驻的，可能已经自动播到更靠后的章，而服务端的进度还停在
        // 上一次落库的位置。这种「续听」场景要以引擎的实时章节为准，否则回到本书
        // 会把正在播的章节往回拽。URL 显式指定 chapter 时尊重 URL（那是明确的跳转）。
        const live = useReaderAudioStore.getState()
        if (
          !explicitChapter &&
          live.bookId === b.id &&
          live.track !== '' &&
          (live.status === 'playing' || live.status === 'loading' || live.status === 'paused') &&
          live.chapterIndex !== idx &&
          chs[live.chapterIndex]
        ) {
          idx = live.chapterIndex
          // 位置由引擎自己掌握，服务端那份属于上一章，别写过去
          pendingPosRef.current = 0
        }
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
  }, [bookId, reloadKey])

  // 正文缓存的键带上书源标识（origin + book_url）：换源后在途的旧源响应即使晚到，
  // 也只会落在旧键上，不会被新源读到。聚合源的不同子源共用同一个 origin（子源写在
  // book_url 里），所以不能只用 origin。
  const contentCacheKey = useCallback(
    (index: number) => `${book?.origin ?? ''}\u0000${book?.book_url ?? ''}\u0000${index}`,
    [book?.origin, book?.book_url],
  )

  // ── 加载章节正文（带缓存与下一章预取） ──
  useEffect(() => {
    if (chapterIndex === null || !book || chapters.length === 0) return
    const ch = chapters[chapterIndex]
    if (!ch) return
    let cancelled = false
    // 换章/换源/离开页面时取消在途请求：慢源一章要等十几秒，不取消就会白等旧源的
    // 响应，换源后还可能把旧源的正文塞进新书的缓存。
    const ac = new AbortController()
    ;(async () => {
      // 每次重新取正文都先清掉上一次的报错。换源时的典型情形：服务端章节缓存刚被清空、
      // 而本地 chapters 还是旧源的，这一次请求必然失败（「章节缓存为空」）；等新源目录
      // 落地后正文 effect 会再来一次并且成功，但不清这里的话，旧错误会一直压在正文上。
      setError('')
      setContent(null)
      setLoadingStage('content')
      try {
        const cacheKey = contentCacheKey(chapterIndex)
        let ct = contentCache.current.get(cacheKey)
        if (!ct) {
          ct = await readerAPI.bookContent(book.id, chapterIndex, ac.signal)
          // 请求期间切了章/换了源：结果已作废，既不用也不必入缓存。
          if (cancelled) return
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
        const savedPos = Math.max(0, Math.floor(pendingPosRef.current))
        if (ct.type !== 'text') {
          setRestorePos(savedPos)
          pendingPosRef.current = 0
        }
        // 听书：把这一章交给常驻播放引擎。引擎是全局单例，离开本页也不会被卸载，
        // 因此换章/离开路由/息屏都继续播（面板卸载不再掐播放）。
        if (ct.type === 'audio' && ct.tracks && ct.tracks.length > 0) {
          getReaderAudioEngine().loadChapter({
            bookId: book.id,
            bookName: book.name,
            cover: book.cover_url ?? '',
            chapters,
            chapterIndex,
            track: ct.tracks[0],
            transcoding: ct.transcoding ?? false,
            openCredits: book.open_credits ?? 0,
            closeCredits: book.close_credits ?? 0,
            initialPos: savedPos,
          })
        }
        // 进度上报（文本的 pos 保留原值，排版完成后才被消费清零）。
        // 注意用 savedPos：非文本在上面已经把 pendingPosRef 清零了，直接读会把
        // 刚恢复的进度又写成 0，退出重进就从头开始。
        readerAPI
          .saveProgress(book.id, { chapter_index: chapterIndex, pos: savedPos, chapter_title: ch.title })
          .catch(() => undefined)
        // 预取下一章
        if (!contentCache.current.has(contentCacheKey(chapterIndex + 1))) {
          readerAPI
            .bookContent(book.id, chapterIndex + 1, ac.signal)
            .then((c) => {
              // 预取是在「旧源」发起、在换源后才回来的话，结果属于脏数据，丢掉。
              if (cancelled) return
              contentCache.current.set(contentCacheKey(chapterIndex + 1), c)
            })
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
      ac.abort()
    }
  }, [chapterIndex, book, chapters, contentCacheKey, contentReloadKey])

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

  // 滚动模式的滚轮改成「类手机滑动」：把滚轮格数累加成目标位置再逐帧逼近，
  // 滚动连续、松手后自己滑行一段，而不是浏览器整格跳变。
  // 翻页模式的滚轮由下面的翻页监听器接管，这里必须关掉。
  useSmoothWheelScroll(scrollRef, settings.pageMode === 'scroll' && contentType === 'text')

  // ── 进度保存（翻页 / 滚动 / 听书 / 漫画） ──
  // pos 对文本是页码、漫画是图片序号、听书是秒；audio.currentTime 带小数，
  // 统一取整后再上报，和 dur_chapter_pos 的 int 语义对齐。
  const savePos = useCallback(
    (pos: number) => {
      if (!book || chapterIndex === null) return
      const ch = chapters[chapterIndex]
      readerAPI
        .saveProgress(book.id, {
          chapter_index: chapterIndex,
          pos: Math.max(0, Math.floor(pos)),
          chapter_title: ch?.title ?? '',
        })
        .catch(() => undefined)
    },
    [book, chapterIndex, chapters],
  )

  useEffect(() => {
    // 只处理文本：听书/漫画的正文为空串（不是 null），不能落到页码保存逻辑里，
    // 否则打开音频章 1.5 秒后就会把页码 0 写成进度，覆盖掉刚才恢复的秒数。
    if (contentType !== 'text' || settings.pageMode !== 'page' || content === null || chapterIndex === null) return
    const t = setTimeout(() => savePos(page), 1500)
    return () => clearTimeout(t)
  }, [page, content, chapterIndex, settings.pageMode, contentType, savePos])

  // 漫画单页进度保存
  useEffect(() => {
    if (contentType !== 'image' || settings.pageMode !== 'page' || !media || chapterIndex === null) return
    const t = setTimeout(() => savePos(comicPage), 1200)
    return () => clearTimeout(t)
  }, [comicPage, contentType, settings.pageMode, media, chapterIndex, savePos])

  // 音频/漫画滚动：节流进度保存（听书 5s、漫画 2s）
  const throttledMediaSave = useCallback(
    (pos: number) => {
      if (!book || chapterIndex === null) return
      const now = Date.now()
      const interval = contentType === 'audio' ? AUDIO_SAVE_INTERVAL_MS : COMIC_SAVE_INTERVAL_MS
      if (now - lastMediaSaveRef.current < interval) return
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

  // ── 目录面板的分段跳转（对齐听书面板的区间下拉） ──
  // 上千章的目录按 100 章一组切开，顶部下拉选区间即整段跳转（见 utils/chapterGroups.ts）。
  // 目录不足一组（≤100 章）时不显示下拉——一屏能扫完，多一个控件只是噪声。
  const tocGroups = useMemo(() => buildChapterGroups(chapters.length), [chapters.length])

  const jumpToTocGroup = useCallback(
    (next: number) => {
      const group = tocGroups[next]
      if (!group) return
      setTocGroupIndex(next)
      tocRef.current?.scrollToIndex({ index: group.start, align: 'start' })
    },
    [tocGroups],
  )

  // 面板开关：打开目录时把区间下拉对齐到当前章节所在组。上次可能停在第 3 组，
  // 续读已到第 12 组，沿用旧值会显示错误的区间。
  const openPanel = useCallback(
    (next: 'none' | 'toc' | 'style') => {
      if (panel === next) {
        setPanel('none')
        return
      }
      if (next === 'toc') setTocGroupIndex(chapterGroupIndexOf(chapterIndex ?? 0, chapters.length))
      setPanel(next)
    },
    [chapterIndex, chapters.length, panel],
  )

  // 听书：引擎自己也可能切章（播完自动下一章、锁屏/耳机按键上一章下一章），把它的
  // 当前章节回写进本页，目录与菜单才不会停在旧章节上。只认本书，避免串到别的书。
  const audioChapterIndex = useReaderAudioStore((s) =>
    book && s.bookId === book.id ? s.chapterIndex : null,
  )
  useEffect(() => {
    if (contentType !== 'audio' || audioChapterIndex === null) return
    if (audioChapterIndex === chapterIndex) return
    pendingEndRef.current = false
    setChapterIndex(audioChapterIndex)
  }, [audioChapterIndex, chapterIndex, contentType])

  // ── 换源（对应 legado 阅读页的「换源」） ──
  //
  // 候选源来自按书名重新搜索；选中后调用换源接口，服务端保留阅读进度、清空旧源
  // 目录缓存，这里只需要清掉正文缓存并让整本书重新加载。
  const openSourcePicker = useCallback(async () => {
    const key = (book?.name ?? '').trim()
    if (!key) {
      toast.error('缺少书名，无法换源')
      return
    }
    setSwitchOpen(true)
    setSwitchLoading(true)
    setSwitchCandidates([])
    try {
      const res = await readerAPI.search(key)
      const list = res.books ?? []
      const wantAuthor = (book?.author ?? '').trim()
      const hit =
        list.find((b) => b.name === key && wantAuthor !== '' && b.author === wantAuthor) ??
        list.find((b) => b.name === key) ??
        null
      setSwitchCandidates(hit?.origins ?? [])
    } catch (e) {
      toast.error((e as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '搜索书源失败')
    } finally {
      setSwitchLoading(false)
    }
  }, [book])

  const applyOrigin = useCallback(
    async (origin: ReaderSearchOrigin) => {
      setSwitchOpen(false)
      if (!book) return
      try {
        await readerAPI.switchOrigin(book.id, origin)
        toast.success(`已切换到「${origin.origin_name || origin.origin}」`)
        contentCache.current.clear()
        // 换源后服务端已清空章节缓存，而本地的 chapters 还是旧源的目录：不清理的话，
        // 重新加载书籍期间（listBooks 已返回、新目录还没抓回来）正文 effect 会拿旧目录
        // 去打新源，撞上「章节缓存为空」。这里先把正文/目录清空，等新源目录到位再取。
        setError('')
        setContent(null)
        setChapters([])
        setReloadKey((v) => v + 1)
      } catch (e) {
        toast.error((e as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '换源失败')
      }
    },
    [book],
  )

  // 本地导入的书没有书源，不显示换源入口
  const canSwitchSource = !!book && !book.is_local

  // 听书：片头/片尾跳过秒数的落库改由播放引擎负责（它持有当前书 ID，离开本页也能写）。

  const goPrev = useCallback(() => {
    if (contentType === 'audio') {
      getReaderAudioEngine().prev()
      return
    }
    if (contentType === 'image' && settings.pageMode === 'page') {
      // 双页：一次退一屏；进度落在该屏的首张图上
      if (comicDoublePage && comicSpreads.length > 0) {
        if (comicSpreadIndex > 0) setComicPage(comicSpreads[comicSpreadIndex - 1][0])
        else goChapter(-1, true)
        return
      }
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
  }, [contentType, settings.pageMode, comicDoublePage, comicSpreads, comicSpreadIndex, comicPage, page, goChapter])

  const goNext = useCallback(() => {
    if (contentType === 'audio') {
      getReaderAudioEngine().next()
      return
    }
    if (contentType === 'image' && settings.pageMode === 'page') {
      if (comicDoublePage && comicSpreads.length > 0) {
        if (comicSpreadIndex < comicSpreads.length - 1) setComicPage(comicSpreads[comicSpreadIndex + 1][0])
        else goChapter(1)
        return
      }
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
  }, [contentType, settings.pageMode, comicDoublePage, comicSpreads, comicSpreadIndex, media, comicPage, page, pageCount, goChapter])

  // ── 点击分区（左 30% 上一页 / 中间呼出菜单 / 右 30% 下一页） ──
  // 翻页模式用覆盖层上的三个按钮驱动；滚动模式不能再用覆盖层——
  // 覆盖层不是滚动容器的子节点，手机上手指落在它上面会让纵向滑动失效
  // （只能点左右），所以滚动模式改在滚动容器自身的 onClick 上按 x 坐标分区：
  // 纵向拖动不会产生 click，浏览器原生滚动照常工作。
  //
  // 手机上再补一层横向滑动：从左往右滑=上一页、从右往左滑=下一页，并且跟手——
  // 滑动途中内容就跟着手指走，松手再决定翻页还是回弹（判定、跟手位移与
  // 「滑动后浏览器补发的 click」的去重都在 useHorizontalSwipe 里）。
  //
  // 跟手拖拽平移的是两个不同的元素：
  // - 文本：正文列外面单独挂一层「跟手层」（pageDragRef）。分页平移仍旧由 React 写在
  //   里层，跟手层只管手指位移；松手时两层同时动——跟手位移归零 + 页码变化——
  //   合起来就是一次连续滑动。分两层而不是直接改里层，是为了避免「跟手位移把 DOM
  //   改成了 React 不知道的值、React 又认为 transform 没变而不去写」把页面留在拖动位置。
  // - 漫画：平移 ReaderComic 的舞台（comicStageRef）。舞台两侧摆着上一页/下一页
  //   （见那边的 carousel 分支），翻页时单元格的 left 偏移与舞台位移在同一次提交里
  //   相抵，画面看不出切换。
  const pageDragRef = useRef<HTMLDivElement>(null)
  const comicStageRef = useRef<HTMLDivElement>(null)
  const pageDragEnabled = settings.pageMode === 'page' && (contentType === 'text' || comicDragSinglePage)

  const onPageDrag = useCallback(
    (dx: number) => {
      const el = contentType === 'image' ? comicStageRef.current : pageDragRef.current
      if (!el) return
      el.style.transition = 'none'
      el.style.transform = `translateX(${dx}px)`
    },
    [contentType],
  )

  const onPageDragEnd = useCallback(
    (dx: number) => {
      if (contentType === 'image') {
        const el = comicStageRef.current
        if (!el) return
        if (dx <= -DRAG_TURN_DISTANCE) {
          settleComicDrag(el, 1, dx, goNext)
          return
        }
        if (dx >= DRAG_TURN_DISTANCE) {
          settleComicDrag(el, -1, dx, goPrev)
          return
        }
        el.style.transition = PAGE_TRANSITION
        el.style.transform = 'translateX(0px)'
        return
      }
      const el = pageDragRef.current
      if (!el) return
      // 跟手层归零与页码平移同时进行，合成一次连续滑动（位移不够时就是回弹）。
      el.style.transition = PAGE_TRANSITION
      el.style.transform = 'translateX(0px)'
      if (dx <= -DRAG_TURN_DISTANCE) goNext()
      else if (dx >= DRAG_TURN_DISTANCE) goPrev()
    },
    [contentType, goNext, goPrev],
  )

  const { onTouchStart, onTouchMove, onTouchEnd, onTouchCancel, consumeSwipe } = useHorizontalSwipe({
    onSwipeRight: goPrev,
    onSwipeLeft: goNext,
    minDistance: DRAG_TURN_DISTANCE,
    onDrag: pageDragEnabled ? onPageDrag : undefined,
    onDragEnd: pageDragEnabled ? onPageDragEnd : undefined,
  })
  const handleZoneTap = useCallback(
    (clientX: number, rect: DOMRect) => {
      const x = rect.width > 0 ? (clientX - rect.left) / rect.width : 0.5
      if (x < 0.3) goPrev()
      else if (x > 0.7) goNext()
      else setMenuOpen((v) => !v)
    },
    [goPrev, goNext],
  )

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
  // 往上滚=上一页，往下滚=下一页。滚动模式的滚轮不在这里处理，
  // 由 useSmoothWheelScroll 接管做平滑惯性滚动。
  // 滚轮必须用原生监听器：React 的 onWheel 是 passive 的，调不了 preventDefault。
  useEffect(() => {
    const el = readerRef.current
    if (!el) return
    // 滚动模式/听书面板/漫画滚动交给各自的滚动逻辑；菜单打开时不翻页
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
  // 正文按行组织成「段落块」：保留原始行号（段评锚点按行号挂靠），
  // 只丢掉既无文字又无段评的空行。
  const blocks = useMemo<ReaderParagraphBlock[]>(() => {
    const lines = (content ?? '').split('\n')
    const byLine = new Map<number, ReaderContentComment[]>()
    for (const c of media?.type === 'text' ? (media.comments ?? []) : []) {
      const list = byLine.get(c.line)
      if (list) list.push(c)
      else byLine.set(c.line, [c])
    }
    const out: ReaderParagraphBlock[] = []
    for (let i = 0; i < lines.length; i++) {
      const text = lines[i].trim()
      const comments = byLine.get(i) ?? []
      if (text === '' && comments.length === 0) continue
      out.push({ text, comments })
    }
    return out
  }, [content, media])

  // 打开段评：服务端带书源 Cookie 抓取评论页，这里用 iframe 承载。
  // 前端直接 window.open 会以未登录身份访问，评论接口拿不到数据。
  const openComment = useCallback(
    async (comment: ReaderContentComment) => {
      if (!book) return
      if (!comment.url) {
        toast('这条评论没有可用地址')
        return
      }
      try {
        const page = await readerAPI.openContentComment({
          book_id: book.id,
          url: comment.url,
          title: comment.label || '段评',
        })
        setBrowserPage(page)
      } catch (e) {
        toast.error(
          (e as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '打开段评失败',
        )
      }
    },
    [book],
  )

  // 正文段落块：普通段落按缩进排版，[img] 行渲染成居中图片，段评在段末挂气泡
  const renderParagraph = (block: ReaderParagraphBlock, key: number) => {
    const { text, comments } = block
    if (text.startsWith(IMG_MARK)) {
      return (
        <p key={key} style={{ marginBottom: settings.paragraphSpacing, textAlign: 'center' }}>
          <img
            src={text.slice(IMG_MARK.length)}
            alt=""
            referrerPolicy="no-referrer"
            style={{ maxWidth: '100%', maxHeight: '70vh', margin: '0 auto', objectFit: 'contain' }}
          />
        </p>
      )
    }
    const standalone = text === '' && comments.length > 0
    return (
      <p
        key={key}
        style={{
          textIndent: text ? '2em' : undefined,
          marginBottom: settings.paragraphSpacing,
          textAlign: standalone && comments.every((c) => c.block) ? 'center' : undefined,
        }}
      >
        {text}
        {comments.map((c, ci) => (
          <button
            key={`${c.line}-${ci}`}
            type="button"
            onClick={(e) => {
              // 滚动容器的分区点击与翻页覆盖层都在上层，不拦住冒泡，
              // 点气泡会顺带翻页或呼出菜单。
              e.stopPropagation()
              void openComment(c)
            }}
            title={c.label || '段评'}
            // 外层正文档关掉了指针事件（见上面的层叠说明），气泡自己开关。
            className="relative z-10 mx-1 inline-flex items-center gap-0.5 rounded-full border align-middle"
            style={{
              borderColor: theme.accent,
              color: theme.accent,
              fontSize: Math.max(10, settings.fontSize - 4),
              lineHeight: 1,
              padding: '2px 6px',
              verticalAlign: 'middle',
              pointerEvents: 'auto',
            }}
          >
            <MessageSquare size={11} />
            {c.count > 0 && <span>{c.count}</span>}
          </button>
        ))}
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
        <div className={`mx-auto h-full w-full ${comicFullWidth ? '' : 'max-w-[900px]'}`}>
          {contentType === 'audio' ? (
            media && media.tracks && media.tracks.length > 0 ? (
              <ReaderAudioPanel theme={theme} onToggleMenu={() => setMenuOpen((v) => !v)} />
            ) : (
              <div className="flex h-full items-center justify-center text-sm opacity-60" style={{ color: theme.text }}>
                {loadingStage !== null ? <Loader2 className="animate-spin opacity-60" size={24} /> : '本章没有可播放的音频'}
              </div>
            )
          ) : contentType === 'image' ? (
            <ReaderComic
              images={comicImages}
              theme={theme}
              mode={settings.pageMode}
              page={comicPage}
              imageFit={comicScrollFit}
              spread={comicDoublePage ? (comicSpreads[comicSpreadIndex] ?? null) : null}
              draggable={comicDragSinglePage}
              stageRef={comicStageRef}
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
               不改高度也就不触发重新分页）。
               relative + z-index + pointer-events-none：正文容器带 transform，
               本身就形成一个层叠上下文，段评气泡的 z-index 只在容器内部生效，
               会被后面的点击覆盖层压住。把整层抬到覆盖层之上并关闭指针事件，
               只让气泡自己 pointer-events:auto，气泡可点、其余位置照样透给
               覆盖层翻页/呼出菜单。 */
            <div
              className={`pointer-events-none relative z-[5] h-full px-4 py-2 ${MENU_SHIFT}`}
              style={menuShiftStyle}
            >
              <div ref={viewportRef} className="relative h-full overflow-hidden">
                {/* 跟手层：跟手拖拽的位移写在这一层，分页平移仍旧写在里层（React 管）。 */}
                <div ref={pageDragRef} className="h-full">
                  <div
                    ref={contentRef}
                    className="h-full"
                    style={{
                      columnWidth: `${Math.max(vw, 1)}px`,
                      columnGap: `${COLUMN_GAP}px`,
                      columnFill: 'auto',
                      transform: `translateX(-${page * (vw + COLUMN_GAP)}px)`,
                      transition: PAGE_TRANSITION,
                      fontSize: settings.fontSize,
                      lineHeight: settings.lineHeight,
                    }}
                  >
                    {blocks.map((b, i) => renderParagraph(b, i))}
                  </div>
                </div>
              </div>
            </div>
          ) : (
            <div
              ref={scrollRef}
              onClick={(e) => handleZoneTap(e.clientX, e.currentTarget.getBoundingClientRect())}
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
              className={`h-full overflow-y-auto overscroll-contain px-4 ${MENU_SHIFT}`}
              style={{ fontSize: settings.fontSize, lineHeight: settings.lineHeight, ...menuShiftStyle }}
            >
              <div className="py-4">
                {blocks.map((b, i) => renderParagraph(b, i))}
              </div>
            </div>
          )}

          {/* 加载 / 错误 / 空内容态 */}
          {(loadingStage !== null || (content !== null && blocks.length === 0)) && contentType === 'text' && (
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
                    contentCache.current.delete(contentCacheKey(chapterIndex))
                    // 用 reload 计数触发重取：把 chapterIndex 设成同一个值不会让
                    // effect 重跑，以前点「重试」只是把错误提示清掉了。
                    setContentReloadKey((v) => v + 1)
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

        {/* 点击分区覆盖层：只在翻页模式用。滚动模式用覆盖层会挡住正文的
            原生纵向滚动（手指落在覆盖层上时找不到可滚动的祖先节点），
            所以滚动模式由正文容器的 onClick 分区（见 handleZoneTap）。 */}
        {settings.pageMode === 'page' && (contentType === 'text' || contentType === 'image') && (
          <div
            className="absolute inset-0 z-[1] grid grid-cols-[30%_40%_30%]"
            onTouchStart={onTouchStart}
            onTouchMove={onTouchMove}
            onTouchEnd={onTouchEnd}
            onTouchCancel={onTouchCancel}
          >
            <button
              type="button"
              aria-label="上一页"
              onClick={() => {
                if (consumeSwipe()) return
                goPrev()
              }}
              className="cursor-w-resize"
            />
            <button
              type="button"
              aria-label="菜单"
              onClick={() => {
                if (consumeSwipe()) return
                setMenuOpen((v) => !v)
              }}
              className="cursor-default"
            />
            <button
              type="button"
              aria-label="下一页"
              onClick={() => {
                if (consumeSwipe()) return
                goNext()
              }}
              className="cursor-e-resize"
            />
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

            {/* 动作行（目录 / 界面 / 夜间 / 模式 / 双页 / 换源）。
                用 flex 等分而不是 grid-cols-N：动作数量随内容类型和书源能力变化，
                grid 列数得写死成字面量类名，多一个按钮就要再加一档。 */}
            <div className="flex pt-1" style={{ color: theme.text }}>
              {([
                { icon: <LayoutList size={18} />, label: '目录', action: () => openPanel('toc') },
                { icon: <BookOpen size={18} />, label: '界面', action: () => openPanel('style') },
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
                // 双页铺开只对漫画的翻页模式有意义；窗口太窄时并排两页没法看，
                // 干脆不显示入口（不是灰掉，避免用户以为坏了）。
                ...(contentType === 'image' && settings.pageMode === 'page' && windowWidth >= COMIC_SPREAD_MIN_WIDTH
                  ? [
                      {
                        icon: <Columns2 size={18} />,
                        label: settings.comicDoublePage ? '单页' : '双页',
                        action: () => settings.setComicDoublePage(!settings.comicDoublePage),
                      },
                    ]
                  : []),
                ...(canSwitchSource
                  ? [
                      {
                        icon: <ArrowLeftRight size={18} />,
                        label: '换源',
                        action: () => {
                          void openSourcePicker()
                        },
                      },
                    ]
                  : []),
              ]).map((item) => (
                <button
                  key={item.label}
                  type="button"
                  onClick={item.action}
                  className="flex flex-1 flex-col items-center gap-1 py-1 opacity-80 hover:opacity-100"
                >
                  {item.icon}
                  <span className="text-2xs">{item.label}</span>
                </button>
              ))}
            </div>

            {/* 界面设置面板（主题 / 字号 / 行距 / 段距 / 漫画图片尺寸） */}
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
                {/* 漫画滚动模式的图片显示尺寸。默认档就是老样子（900px 居中），
                    「适应宽度」铺满窗口，「原图」按原始像素 1:1 看细节。 */}
                {showComicImageFit && (
                  <div className="mt-3 flex flex-wrap items-center gap-2 text-xs">
                    <span className="opacity-70">图片尺寸</span>
                    {COMIC_IMAGE_FITS.map((item) => {
                      const active = settings.comicImageFit === item.id
                      return (
                        <button
                          key={item.id}
                          type="button"
                          onClick={() => settings.setComicImageFit(item.id)}
                          className={`rounded-lg border px-2 py-0.5 ${active ? 'font-bold' : 'opacity-70'}`}
                          style={{
                            borderColor: active ? theme.accent : theme.text + '44',
                            color: active ? theme.accent : theme.text,
                          }}
                        >
                          {item.label}
                        </button>
                      )
                    })}
                  </div>
                )}
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
                <p className="min-w-0 flex-1 truncate text-sm font-bold">
                  {book?.name ?? '目录'}
                  <span className="ml-2 text-2xs font-normal opacity-60">目录（{chapters.length} 章）</span>
                </p>
                {tocGroups.length > 1 && (
                  <select
                    value={tocGroupIndex}
                    onChange={(e) => jumpToTocGroup(Number(e.target.value))}
                    aria-label="按区间快速定位章节"
                    title="按区间快速定位章节"
                    className="shrink-0 rounded-lg border bg-transparent px-2 py-1 text-2xs outline-none"
                    style={{ borderColor: theme.text + '33', color: theme.text }}
                  >
                    {tocGroups.map((group) => (
                      <option
                        key={group.index}
                        value={group.index}
                        style={{ color: '#111827', backgroundColor: '#ffffff' }}
                      >
                        {group.label}
                      </option>
                    ))}
                  </select>
                )}
              </div>
              <div className="min-h-0 flex-1">
                <Virtuoso
                  ref={tocRef}
                  data={chapters}
                  initialTopMostItemIndex={Math.max(0, chapterIndex ?? 0)}
                  rangeChanged={(range) =>
                    setTocGroupIndex(chapterGroupIndexOf(range.startIndex, chapters.length))
                  }
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

      {switchOpen && (
        <SourcePickerDialog
          title={book?.name ?? '这本书'}
          origins={switchCandidates}
          current={{ originURL: book?.origin, bookURL: book?.book_url }}
          loading={switchLoading}
          emptyHint="按书名重搜后没有找到其它书源"
          onPick={(origin) => {
            void applyOrigin(origin)
          }}
          onClose={() => setSwitchOpen(false)}
        />
      )}

      {/* 段评评论页：复用书源页面的承载面板（带书源 Cookie 由服务端抓取） */}
      {browserPage && <BrowserPanel page={browserPage} onClose={() => setBrowserPage(null)} />}
    </div>
  )
}
