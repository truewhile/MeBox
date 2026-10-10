import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties } from 'react'
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
  Maximize,
  MessageSquare,
  Minimize,
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
import { useDoubleTap } from '../../hooks/useDoubleTap'
import { useHorizontalSwipe } from '../../hooks/useHorizontalSwipe'
import { useIsTouchDevice } from '../../hooks/useIsTouchDevice'
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

/**
 * 正文「保持真实渲染」的页窗口：当前页 ± 这么多页的段落不做 content-visibility 跳过。
 *
 * 为什么要有这个窗口：被跳过的段落不能每帧重算可见性。翻页/拖动时内容每移动一点，
 * 进入或离开视口的段落都要重新判定，而每次判定变化都会重排整条多栏流——实测一次
 * 滑动触发 32 次 layout（20 次滑动主线程 10.6s，JS 只占 0.014s），帧根本画不完，
 * 表现出来就是两页之间抖。窗口固定住以后，动画期间不再有可见性变化，帧就稳了；
 * 只有读完窗口（每 TEXT_WARM_SHIFT 页）才付一次重排。
 */
const TEXT_WARM_PAGES = 12
/** 页码漂出这个范围才移动渲染窗口，避免每翻一页都改可见性触发重排。 */
const TEXT_WARM_SHIFT = 6

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

/**
 * 翻页模式正文列：与页码 state 解耦。
 *
 * 长章（几百页 CSS 多栏）里，若每次 setPage 都把整棵段落树再调和一遍，
 * 手机主线程会卡数百毫秒，表现为「松手停一下才翻到下一页」。
 * 分页位移改走 contentRef 命令式写入；这里用 memo，页码变时跳过段落重渲染。
 */
const ReaderTextPageBody = memo(function ReaderTextPageBody({
  blocks,
  paragraphSpacing,
  fontSize,
  accent,
  onOpenComment,
}: {
  blocks: ReaderParagraphBlock[]
  paragraphSpacing: number
  fontSize: number
  accent: string
  onOpenComment: (comment: ReaderContentComment) => void
}) {
  return (
    <>
      {blocks.map((block, key) => {
        const { text, comments } = block
        // content-visibility：跳过视口外段落的绘制，否则每次 transform 都要重绘整章
        // （实测可到数百毫秒）。默认全部按需跳过；当前页附近的段落由
        // applyParagraphWarmWindow 改成 visible 并固定成一个窗口——被跳过的段落
        // 会在每帧重新判定可见性，导致动画期间反复重排（见 TEXT_WARM_PAGES）。
        const skipPaint: CSSProperties = {
          contentVisibility: 'auto',
          containIntrinsicSize: 'auto 4em',
        }
        if (text.startsWith(IMG_MARK)) {
          return (
            <p key={key} style={{ marginBottom: paragraphSpacing, textAlign: 'center', ...skipPaint }}>
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
              marginBottom: paragraphSpacing,
              textAlign: standalone && comments.every((c) => c.block) ? 'center' : undefined,
              ...skipPaint,
            }}
          >
            {text}
            {comments.map((c, ci) => (
              <button
                key={`${c.line}-${ci}`}
                type="button"
                onClick={(e) => {
                  e.stopPropagation()
                  onOpenComment(c)
                }}
                title={c.label || '段评'}
                className="relative z-10 mx-1 inline-flex items-center gap-0.5 rounded-full border align-middle"
                style={{
                  borderColor: accent,
                  color: accent,
                  fontSize: Math.max(10, fontSize - 4),
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
      })}
    </>
  )
})

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
 *
 * base 是手势开始时舞台已经带着的残余位移（上一次归位动画没滑完就又被接管）。
 */
function settleComicDrag(el: HTMLDivElement, turn: 1 | -1, dx: number, base: number, turnPage: () => void): void {
  const width = el.clientWidth || 1
  el.style.transition = 'none'
  el.style.transform = `translateX(${base + dx + turn * width}px)`
  void el.offsetWidth
  el.style.transition = PAGE_TRANSITION
  el.style.transform = 'translateX(0px)'
  turnPage()
}

/**
 * 读元素当前的实际横向位移。
 *
 * 跟手层/漫画舞台的归位是 CSS 过渡，一次手势可能在过渡中途就被下一次手势接管。
 * 那时元素的 transform 停在中间值，必须把它读出来当作新手势的起点，
 * 否则新手势从 dx 重新开始，残余位移被抹掉，画面就会跳一格——快速连续翻页时
 * 最明显，看起来就是两页之间来回抖。
 */
function readTranslateX(el: HTMLElement): number {
  const t = getComputedStyle(el).transform
  if (!t || t === 'none') return 0
  const nums = t.match(/-?[\d.]+(?:e[-+]?\d+)?/gi)
  if (!nums) return 0
  // translate3d 在 Chrome 里可能给 matrix(a,b,c,d,tx,ty) 也可能给 matrix3d(...)，
  // 两种都要取横向平移分量。
  const v = nums.length >= 16 ? Number(nums[12]) : nums.length >= 6 ? Number(nums[4]) : 0
  return Number.isFinite(v) ? v : 0
}

/**
 * 文本滑动松手后的跟手层归位。正文多栏已瞬移，这里只把跟手层从「画面连续」的
 * 位移滑回 0。不用 void offsetWidth 强刷布局——长章多栏一强制回流就要几百毫秒。
 * turn=1 下一页，turn=-1 上一页；dx 为松手时的跟手位移。
 *
 * 起点必须是 base + dx + turn*stride：
 * - stride 要和正文那一跳用的步长完全一致（列宽 + 列间距）。用列宽代替步长的话，
 *   松手瞬间跟手层会少补一个列间距，画面先往回弹 48px 再往前滑。
 * - base 是手势开始时跟手层已经带着的残余位移：上一次归位动画没滑完就又滑了一次时，
 *   不带上它就等于把这段残余抹掉，画面跳一格。快速连续翻页时最明显。
 */
function settleTextDrag(dragEl: HTMLDivElement, turn: 1 | -1, dx: number, stride: number, base: number): void {
  const step = stride > 0 ? stride : dragEl.clientWidth || 1
  dragEl.style.transition = 'none'
  dragEl.style.transform = `translate3d(${base + dx + turn * step}px,0,0)`
  requestAnimationFrame(() => {
    dragEl.style.transition = 'transform 140ms ease-out'
    dragEl.style.transform = 'translate3d(0,0,0)'
  })
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
  // 浏览器全屏（沉浸式）：把阅读层整个铺满屏幕，连浏览器的地址栏/状态栏一起让出来。
  // 页面本身是 fixed inset-0，所以这里要解决的是浏览器自身的界面，而不是页面内的留白。
  const [fullscreen, setFullscreen] = useState(false)
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
  /** 文本跟手层：滑动动画也写在这一层，避免给整章多栏做 transition。 */
  const pageDragRef = useRef<HTMLDivElement>(null)
  const [page, setPage] = useState(0)
  const [pageCount, setPageCount] = useState(1)
  const [vw, setVw] = useState(0)
  // 文本翻页：位移与页脚先走 DOM，React 的 page state 等到过渡结束后再同步。
  // 若在同一次点击/触摸里同步 setPage，整页重渲染会堵住下一次绘制，
  // 命令式 transform 也要等卡完才开始——点按和滑动都会顿一下。
  const vwRef = useRef(0)
  const pageRef = useRef(0)
  const pageCountRef = useRef(1)
  const chapterTitleRef = useRef('')
  const pageLabelRef = useRef<HTMLDivElement>(null)
  const appliedTextPageRef = useRef(-1)
  const appliedTextVwRef = useRef(-1)
  const snapTextPageRef = useRef(false)
  /** 把 pageRef 同步回 React state 的防抖定时器；翻页热路径上绝不同步 setPage。 */
  const textPageSyncTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  /** 首次分页完成后锁住 pageCount，避免 content-visibility 改变 scrollWidth 后反复 setPageCount。 */
  const pageCountLockedRef = useRef(false)
  /** 正文段落节点与它们各自落在的页号，用来算「当前页附近的渲染窗口」。 */
  const paragraphsRef = useRef<NodeListOf<HTMLParagraphElement> | null>(null)
  const paragraphColsRef = useRef<Int32Array | null>(null)
  /** 每个段落当前是不是「真实渲染」（1=visible，0=按需跳过），只在分类变化时写 DOM。 */
  const paragraphWarmRef = useRef<Uint8Array | null>(null)
  /** 当前窗口中心页；-1 表示窗口失效（映射刚重算过），下次应用时重建。 */
  const warmCenterRef = useRef(-1)
  vwRef.current = vw
  pageCountRef.current = pageCount
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

  // ── 浏览器全屏（沉浸式） ──
  // 只认标准 API：iOS 上的 Safari 至今不支持元素全屏（只有 <video> 能全屏），
  // 所以那里不显示按钮（不是灰掉——按钮点了没反应会被当成坏了）。
  const fullscreenSupported = useMemo(
    () => typeof document !== 'undefined' && document.fullscreenEnabled === true,
    [],
  )
  useEffect(() => {
    const onFullscreenChange = () => setFullscreen(Boolean(document.fullscreenElement))
    document.addEventListener('fullscreenchange', onFullscreenChange)
    return () => document.removeEventListener('fullscreenchange', onFullscreenChange)
  }, [])
  // 离开阅读页时退出全屏：否则返回书架后浏览器仍停在全屏，整站都被罩住。
  useEffect(
    () => () => {
      if (document.fullscreenElement) void document.exitFullscreen()
    },
    [],
  )
  const toggleFullscreen = useCallback(() => {
    const el = readerRef.current
    if (!el) return
    if (document.fullscreenElement) {
      void document.exitFullscreen()
      return
    }
    // 进全屏是为了看正文，菜单还压在上面就没意义了，先收起来。
    setMenuOpen(false)
    setPanel('none')
    // 浏览器可能以权限策略等理由拒绝，静默忽略即可（按钮状态由 fullscreenchange 回写）。
    void el.requestFullscreen?.().catch(() => {})
  }, [])

  // ── 手机端：中间区域双击进出全屏 ──
  // 只在「触摸设备 + 浏览器支持元素全屏」时启用：桌面端不装这套（点击不带任何延迟，
  // 保持原来的操作手感），iOS Safari 不支持元素全屏（见上）也照旧只留单击呼出菜单。
  // 区分单击/双击的等待与「第二下同步调 requestFullscreen」的约束都在 useDoubleTap 里。
  const touchDevice = useIsTouchDevice()
  const doubleTapFullscreen = fullscreenSupported && touchDevice
  const { tap: centerTap, arm: armCenterDoubleTap, cancel: cancelCenterTap } = useDoubleTap({
    // 单击仍是呼出/收起菜单，双击改成进出浏览器全屏。
    onSingleTap: () => setMenuOpen((v) => !v),
    onDoubleTap: toggleFullscreen,
    enabled: doubleTapFullscreen,
  })

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
        // 换章瞬移到目标页，不要带着上一章的翻页过渡滑过去。
        snapTextPageRef.current = true
        appliedTextPageRef.current = -1
        pageCountLockedRef.current = false
        pageRef.current = 0
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
  /** 一页正文占的横向步长：列宽 + 列间距。分页位移和跟手补位必须用同一个值。 */
  const textPageStride = useCallback(() => {
    const width = vwRef.current > 0
      ? vwRef.current
      : Math.round(viewportRef.current?.getBoundingClientRect().width ?? 0)
    return Math.max(width, 1) + COLUMN_GAP
  }, [])

  /** 把正文列平移到指定页。animate=true 时走 220ms 过渡；换章/重排传 false。 */
  const applyTextPageTransform = useCallback(
    (nextPage: number, animate: boolean) => {
      const el = contentRef.current
      if (!el) return
      const stride = textPageStride()
      el.style.transition = animate ? PAGE_TRANSITION : 'none'
      // 正文多栏可能极宽，不要 translate3d / will-change，免得整章被提成巨大合成层。
      el.style.transform = `translateX(-${nextPage * stride}px)`
      appliedTextPageRef.current = nextPage
      appliedTextVwRef.current = vwRef.current > 0 ? vwRef.current : (viewportRef.current?.clientWidth ?? 0)
    },
    [textPageStride],
  )

  const updateTextPageLabel = useCallback((nextPage: number) => {
    const label = pageLabelRef.current
    if (!label) return
    const title = chapterTitleRef.current
    label.textContent = title ? `${nextPage + 1} / ${pageCountRef.current} · ${title}` : `${nextPage + 1} / ${pageCountRef.current}`
  }, [])

  /**
   * 重算「段落 → 页号」映射。正文是一条超长多栏流，段落落在哪一栏只能从布局读。
   * 2500 段读一遍 offsetLeft 约 2ms（布局干净时），比让浏览器每帧自己重算便宜得多。
   */
  const refreshParagraphColumns = useCallback(() => {
    const el = contentRef.current
    if (!el) return
    const ps = el.querySelectorAll('p')
    const stride = textPageStride()
    const cols = new Int32Array(ps.length)
    for (let i = 0; i < ps.length; i++) cols[i] = Math.floor(ps[i].offsetLeft / stride)
    paragraphsRef.current = ps
    paragraphColsRef.current = cols
    // 映射变了，缓存的「谁在渲染」标记不能沿用：下次应用时以 DOM 现状为基准重算。
    paragraphWarmRef.current = new Uint8Array(ps.length)
    warmCenterRef.current = -1
  }, [textPageStride])

  /**
   * 把当前页附近的段落固定成「真实渲染」，其余继续按需跳过。
   * 只有分类真的变了的段落才写 DOM，所以窗口不动时这个函数是空转。
   */
  const applyParagraphWarmWindow = useCallback(
    (nextPage: number) => {
      if (settings.pageMode !== 'page' || contentType !== 'text') return
      const cols = paragraphColsRef.current
      const ps = paragraphsRef.current
      const warm = paragraphWarmRef.current
      if (!cols || !ps || !warm || cols.length !== ps.length) return
      const center = warmCenterRef.current
      if (center >= 0 && Math.abs(nextPage - center) <= TEXT_WARM_SHIFT) return
      if (center < 0) {
        for (let i = 0; i < ps.length; i++) warm[i] = ps[i].style.contentVisibility === 'visible' ? 1 : 0
      }
      warmCenterRef.current = nextPage
      const lo = nextPage - TEXT_WARM_PAGES
      const hi = nextPage + TEXT_WARM_PAGES
      let flipped = false
      for (let i = 0; i < ps.length; i++) {
        const want = cols[i] >= lo && cols[i] <= hi ? 1 : 0
        if (warm[i] === want) continue
        warm[i] = want
        flipped = true
        // 放进窗口 = visible（真实渲染），移出窗口 = auto（按需跳过，高度用记住的值）。
        ps[i].style.setProperty('content-visibility', want ? 'visible' : 'auto')
      }
      // 可见性一变，段落高度就从估算值换成真实值，后面所有段落都会挪位，
      // 缓存的映射随即过期。等下一帧布局落定再重算，别卡在这一帧里。
      if (flipped) requestAnimationFrame(() => refreshParagraphColumns())
    },
    [settings.pageMode, contentType, refreshParagraphColumns],
  )

  /** 退出翻页模式时把整章段落放回按需跳过，别留着上一次的渲染窗口。 */
  const resetParagraphWarmWindow = useCallback(() => {
    const ps = paragraphsRef.current
    const warm = paragraphWarmRef.current
    if (!ps || !warm) return
    for (let i = 0; i < ps.length; i++) {
      if (!warm[i]) continue
      warm[i] = 0
      ps[i].style.setProperty('content-visibility', 'auto')
    }
    warmCenterRef.current = -1
  }, [])

  /**
   * 把 pageRef 写回 React。长章每次 setPage 都会让整页重渲染卡住 1s+，
   * 所以阅读翻页热路径绝不调用；只在打开菜单 / 换章 / 卸载时同步。
   */
  const flushTextPageReactSync = useCallback(() => {
    if (textPageSyncTimerRef.current !== null) {
      clearTimeout(textPageSyncTimerRef.current)
      textPageSyncTimerRef.current = null
    }
    setPage((p) => (p === pageRef.current ? p : pageRef.current))
  }, [])

  /**
   * 文本翻到指定页（热路径零 setState）。
   * - 正文多栏瞬移 + 页脚 DOM 更新
   * - 点按/滚轮：无动画
   * - 滑动松手：只动画跟手层
   * dragDx 有值表示来自滑动松手。
   */
  const commitTextPage = useCallback(
    (nextPage: number, opts?: { dragDx?: number; dragBase?: number }) => {
      const from = pageRef.current
      const dragDx = opts?.dragDx
      const fromSwipe = typeof dragDx === 'number'
      const turn: 1 | -1 = nextPage >= from ? 1 : -1

      pageRef.current = nextPage
      applyTextPageTransform(nextPage, false)
      updateTextPageLabel(nextPage)
      applyParagraphWarmWindow(nextPage)

      const dragEl = pageDragRef.current
      if (fromSwipe && dragEl && nextPage !== from) {
        settleTextDrag(dragEl, turn, dragDx, textPageStride(), opts?.dragBase ?? 0)
      } else if (dragEl) {
        // 点按翻页也可能打断上一次的归位动画。这里不能用 transition:'none' 直接抹掉残余：
        // 那是硬跳。带上过渡滑回 0，起点就是当前的实际位置，画面才是连续的。
        dragEl.style.transition = PAGE_TRANSITION
        dragEl.style.transform = 'translate3d(0,0,0)'
      }
    },
    [applyTextPageTransform, updateTextPageLabel, applyParagraphWarmWindow, textPageStride],
  )

  const relayout = useCallback(() => {
    const vp = viewportRef.current
    const el = contentRef.current
    if (!vp || !el) return
    // 列宽必须是整数、而且要和位移用的步长完全一致。
    // clientWidth / state 是取整的，而实际布局宽度可能是小数（缩放、非整数 DPI 很常见，
    // 比如 382.4）。容器宽 382.4、column-width 写 382 时，浏览器按容器宽排出 382.4 的列，
    // 真实列距就变成 430.4，而位移按 430 算——每页差 0.4px，误差乘以页码：
    // 第 339 页就偏了 135px，整页文字从句子中间切开。所以这里把容器宽钉成同一个整数，
    // 真实列距就等于步长，页码再大也不会漂。
    const width = Math.max(1, Math.round(vp.getBoundingClientRect().width))
    const widthPx = `${width}px`
    // 宽度和列宽都用命令式写：React 那边用的是 vw state，晚一帧才跟上，
    // 这一帧里算出来的 scrollWidth / 页数就会是错的。
    if (el.style.width !== widthPx) el.style.width = widthPx
    if (el.style.columnWidth !== widthPx) el.style.columnWidth = widthPx
    // 用正文列自己的 scrollWidth；视口在子元素 transform 时 scrollWidth 会抖，
    // 导致翻一页就重新 setPageCount → 整页重渲染卡 1s+。
    const total = el.scrollWidth ?? vp.scrollWidth
    const count = Math.max(1, Math.ceil((total + COLUMN_GAP) / (width + COLUMN_GAP)))

    const widthChanged = width !== vwRef.current
    if (widthChanged) pageCountLockedRef.current = false
    // content-visibility 只给视口附近的段落算真尺寸，远端的段落按估算高度参与分页，
    // 所以 scrollWidth 会随阅读单向变大。锁定期内只接受变大（页数更准，且增长是单向
    // 的，不会来回抖）；不接受变小——正文一跳回估算值就会把页码夹回上一页，
    // 表现出来就是「翻到下一页又被拉回上一页」。
    const effectiveCount = pageCountLockedRef.current ? Math.max(count, pageCountRef.current) : count
    const countGrew = effectiveCount !== pageCountRef.current

    const pendingEnd = pendingEndRef.current && effectiveCount > 0
    const pendingPos = pendingPosRef.current > 0
    const clamped = Math.min(pageRef.current, Math.max(0, effectiveCount - 1))
    const nextPage = pendingEnd
      ? effectiveCount - 1
      : pendingPos
        ? Math.min(pendingPosRef.current, effectiveCount - 1)
        : clamped

    if (pendingEnd) pendingEndRef.current = false
    if (pendingPos) pendingPosRef.current = 0

    const pageChanged = nextPage !== pageRef.current

    // 热路径上尺寸没变就别 setState——否则每次翻页都会重渲染 2000+ 段正文。
    if (!widthChanged && !countGrew && !pageChanged && !pendingEnd && !pendingPos) {
      if (count > 1) pageCountLockedRef.current = true
      return
    }

    vwRef.current = width
    pageCountRef.current = effectiveCount
    pageRef.current = nextPage
    applyParagraphWarmWindow(nextPage)
    if (widthChanged) setVw(width)
    if (countGrew) {
      setPageCount(effectiveCount)
      if (effectiveCount > 1) pageCountLockedRef.current = true
    }
    if (pageChanged || pendingEnd || pendingPos) {
      snapTextPageRef.current = true
      setPage(nextPage)
      updateTextPageLabel(nextPage)
      applyTextPageTransform(nextPage, false)
    }
  }, [applyTextPageTransform, updateTextPageLabel, applyParagraphWarmWindow])

  useLayoutEffect(() => {
    relayout()
    // 正文/字号/列宽变了：段落落位全变了，重新算映射并重建渲染窗口。
    if (settings.pageMode === 'page' && contentType === 'text') {
      refreshParagraphColumns()
      applyParagraphWarmWindow(pageRef.current)
    } else {
      resetParagraphWarmWindow()
    }
  }, [relayout, refreshParagraphColumns, applyParagraphWarmWindow, resetParagraphWarmWindow, content, settings.fontSize, settings.lineHeight, settings.paragraphSpacing, settings.pageMode, contentType, vw])

  // 页脚页码的文字只由 DOM 直接写（翻页热路径不 setState），React 那边不渲染子节点。
  // 两边都写的话，React 重渲染时会拿滞后的 page 去覆盖，覆盖失败时页面会闪一下旧页码。
  // 这里在换章 / 页数变化时补写一次，保证页脚不为空。
  useLayoutEffect(() => {
    if (settings.pageMode !== 'page' || contentType !== 'text') return
    updateTextPageLabel(pageRef.current)
  }, [content, pageCount, chapterIndex, contentType, settings.pageMode, updateTextPageLabel])

  // 列宽变化或换章恢复时对齐位移。
  // 阅读热路径以 pageRef 为权威页码（不走 setPage）；这里绝不能用滞后的 React page
  // 反写 pageRef，否则会出现「翻到下一页又被拉回上一页」的来回抖动。
  useLayoutEffect(() => {
    if (contentType !== 'text' || settings.pageMode !== 'page') return
    if (snapTextPageRef.current) {
      snapTextPageRef.current = false
      pageRef.current = page
      applyTextPageTransform(page, false)
      updateTextPageLabel(page)
      return
    }
    // React page 还没跟上热路径：只在宽度变了时按 pageRef 重算位移。
    if (page !== pageRef.current) {
      if (appliedTextVwRef.current !== vw) {
        applyTextPageTransform(pageRef.current, false)
        updateTextPageLabel(pageRef.current)
      }
      return
    }
    if (appliedTextPageRef.current === page && appliedTextVwRef.current === vw) return
    applyTextPageTransform(page, false)
    updateTextPageLabel(page)
  }, [page, vw, contentType, settings.pageMode, applyTextPageTransform, updateTextPageLabel])

  useEffect(() => {
    const vp = viewportRef.current
    if (!vp) return
    // 合并同一帧里多次 resize，避免字体/滚动条抖动连带 relayout→setState。
    let raf = 0
    const ro = new ResizeObserver(() => {
      if (raf) cancelAnimationFrame(raf)
      raf = requestAnimationFrame(() => {
        raf = 0
        relayout()
      })
    })
    ro.observe(vp)
    return () => {
      if (raf) cancelAnimationFrame(raf)
      ro.disconnect()
    }
  }, [relayout, content, settings.pageMode])

  // 打开菜单时把 pageRef 写回 state，进度条才不会停在旧页。
  useEffect(() => {
    if (menuOpen) flushTextPageReactSync()
  }, [menuOpen, flushTextPageReactSync])

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

  // 文本翻页进度：不依赖 React page（翻页热路径不 setPage），定时读 pageRef。
  useEffect(() => {
    if (contentType !== 'text' || settings.pageMode !== 'page' || content === null || chapterIndex === null) return
    let last = -1
    const tick = () => {
      const pos = pageRef.current
      if (pos === last) return
      last = pos
      savePos(pos)
    }
    const t = setInterval(tick, 2000)
    return () => {
      clearInterval(t)
      tick()
    }
  }, [content, chapterIndex, settings.pageMode, contentType, savePos])

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
    // 文本翻页读 pageRef：动画期间 React page 可能还没跟上，连点不能被旧 state 卡住。
    if (pageRef.current > 0) commitTextPage(pageRef.current - 1)
    else goChapter(-1, true)
  }, [contentType, settings.pageMode, comicDoublePage, comicSpreads, comicSpreadIndex, comicPage, goChapter, commitTextPage])

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
    if (pageRef.current < pageCountRef.current - 1) commitTextPage(pageRef.current + 1)
    else goChapter(1)
  }, [contentType, settings.pageMode, comicDoublePage, comicSpreads, comicSpreadIndex, media, comicPage, goChapter, commitTextPage])

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
  // - 文本：正文列外面单独挂一层「跟手层」（pageDragRef）。正文多栏只瞬移，
  //   滑页动画全在跟手层（settleTextDrag）；长章不能给多栏做 transition。
  // - 漫画：平移 ReaderComic 的舞台（comicStageRef）。舞台两侧摆着上一页/下一页
  //   （见那边的 carousel 分支），翻页时单元格的 left 偏移与舞台位移在同一次提交里
  //   相抵，画面看不出切换。
  const comicStageRef = useRef<HTMLDivElement>(null)
  const pageDragEnabled = settings.pageMode === 'page' && (contentType === 'text' || comicDragSinglePage)
  /**
   * 本次跟手手势开始时，跟手层/舞台已经带着的残余位移。
   * 上一次翻页的归位动画没滑完就又滑了一次，新位移必须接着它算，否则画面跳一格。
   */
  const dragBaseRef = useRef(0)

  const onPageDragStart = useCallback(() => {
    const el = contentType === 'image' ? comicStageRef.current : pageDragRef.current
    dragBaseRef.current = el ? readTranslateX(el) : 0
  }, [contentType])

  const onPageDrag = useCallback(
    (dx: number) => {
      const el = contentType === 'image' ? comicStageRef.current : pageDragRef.current
      if (!el) return
      el.style.transition = 'none'
      el.style.transform = `translate3d(${dragBaseRef.current + dx}px,0,0)`
    },
    [contentType],
  )

  const onPageDragEnd = useCallback(
    (dx: number) => {
      const base = dragBaseRef.current
      dragBaseRef.current = 0
      if (contentType === 'image') {
        const el = comicStageRef.current
        if (!el) return
        if (dx <= -DRAG_TURN_DISTANCE) {
          settleComicDrag(el, 1, dx, base, goNext)
          return
        }
        if (dx >= DRAG_TURN_DISTANCE) {
          settleComicDrag(el, -1, dx, base, goPrev)
          return
        }
        el.style.transition = PAGE_TRANSITION
        el.style.transform = 'translateX(0px)'
        return
      }
      const el = pageDragRef.current
      if (!el) return
      if (dx <= -DRAG_TURN_DISTANCE) {
        if (pageRef.current < pageCountRef.current - 1) {
          commitTextPage(pageRef.current + 1, { dragDx: dx, dragBase: base })
        } else {
          el.style.transition = PAGE_TRANSITION
          el.style.transform = 'translate3d(0,0,0)'
          goChapter(1)
        }
        return
      }
      if (dx >= DRAG_TURN_DISTANCE) {
        if (pageRef.current > 0) {
          commitTextPage(pageRef.current - 1, { dragDx: dx, dragBase: base })
        } else {
          el.style.transition = PAGE_TRANSITION
          el.style.transform = 'translate3d(0,0,0)'
          goChapter(-1, true)
        }
        return
      }
      // 位移不够：回弹，不翻页。
      el.style.transition = PAGE_TRANSITION
      el.style.transform = 'translate3d(0,0,0)'
    },
    [contentType, goNext, goPrev, commitTextPage, goChapter],
  )

  const { onTouchStart, onTouchMove, onTouchEnd, onTouchCancel, consumeSwipe } = useHorizontalSwipe({
    onSwipeRight: goPrev,
    onSwipeLeft: goNext,
    minDistance: DRAG_TURN_DISTANCE,
    onDragStart: pageDragEnabled ? onPageDragStart : undefined,
    onDrag: pageDragEnabled ? onPageDrag : undefined,
    onDragEnd: pageDragEnabled ? onPageDragEnd : undefined,
  })
  const handleZoneTap = useCallback(
    (clientX: number, rect: DOMRect) => {
      const x = rect.width > 0 ? (clientX - rect.left) / rect.width : 0.5
      if (x < 0.3) {
        cancelCenterTap()
        goPrev()
      } else if (x > 0.7) {
        cancelCenterTap()
        goNext()
      } else {
        // 中间单击=菜单、双击=全屏，区分逻辑在 useDoubleTap（桌面端原样单击）。
        centerTap()
      }
    },
    [goPrev, goNext, centerTap, cancelCenterTap],
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
  chapterTitleRef.current = currentChapter?.title ?? ''

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
        if (settings.pageMode === 'page') {
          commitTextPage(v - 1)
          flushTextPageReactSync()
        }
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

  // 段评点击：ReaderTextPageBody 要稳定回调，否则 memo 每次都失效。
  const handleOpenComment = useCallback(
    (comment: ReaderContentComment) => {
      void openComment(comment)
    },
    [openComment],
  )

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
    <div
      ref={readerRef}
      className="fixed inset-0 z-40 flex flex-col"
      // 手机上双击中间切全屏时，别让浏览器把自己的「双击缩放」也做一遍：
      // touch-action:manipulation 关掉双击缩放，纵向滚动与双指缩放照常。
      style={{ backgroundColor: theme.bg, color: theme.text, touchAction: doubleTapFullscreen ? 'manipulation' : undefined }}
    >
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
                if (zone === 'center') centerTap()
                // 点了左右分区（翻屏）就丢掉待判定的中间单击，别让它在窗口期满时弹菜单。
                else cancelCenterTap()
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
                {/* 跟手层：跟手位移写这里；分页平移由 applyTextPageTransform 写在 contentRef 上。 */}
                <div ref={pageDragRef} className="h-full">
                  <div
                    ref={contentRef}
                    className="h-full"
                    style={{
                      columnWidth: `${Math.max(vw, 1)}px`,
                      columnGap: `${COLUMN_GAP}px`,
                      columnFill: 'auto',
                      fontSize: settings.fontSize,
                      lineHeight: settings.lineHeight,
                    }}
                  >
                    <ReaderTextPageBody
                      blocks={blocks}
                      paragraphSpacing={settings.paragraphSpacing}
                      fontSize={settings.fontSize}
                      accent={theme.accent}
                      onOpenComment={handleOpenComment}
                    />
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
                <ReaderTextPageBody
                  blocks={blocks}
                  paragraphSpacing={settings.paragraphSpacing}
                  fontSize={settings.fontSize}
                  accent={theme.accent}
                  onOpenComment={handleOpenComment}
                />
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
                cancelCenterTap()
                goPrev()
              }}
              className="cursor-w-resize"
            />
            <button
              type="button"
              aria-label="菜单"
              onClick={() => {
                if (consumeSwipe()) return
                // 中间单击=菜单、双击=全屏，区分逻辑在 useDoubleTap（桌面端原样单击）。
                centerTap()
              }}
              className="cursor-default"
            />
            <button
              type="button"
              aria-label="下一页"
              onClick={() => {
                if (consumeSwipe()) return
                cancelCenterTap()
                goNext()
              }}
              className="cursor-e-resize"
            />
          </div>
        )}
      </div>

      {/* 页脚页码（翻页模式）。文字由 updateTextPageLabel 直接写 DOM，
          React 不渲染子节点，避免重渲染时用滞后的页码覆盖。 */}
      {settings.pageMode === 'page' && contentType === 'text' && content !== null && (
        <div
          ref={pageLabelRef}
          className="pointer-events-none pb-2 text-center text-2xs opacity-50"
          style={{ color: theme.text }}
        />
      )}

      {/* 主菜单（仿 legado ReadMenu） */}
      {menuOpen && (
        <>
          <button
            type="button"
            aria-label="关闭菜单"
            className="fixed inset-0 z-40 cursor-default bg-black/30"
            onClick={(e) => {
              // 关菜单要立刻生效，不套单击延迟。
              setMenuOpen(false)
              // 但若这一下关在中间区域，就顺手开个双击窗口：菜单开着时双击中间
              // 也能切全屏（第二下落在刚关掉菜单的正文上，见 useDoubleTap.arm）。
              // 点在左右三分区（本意是翻页）时不开窗口，免得被下一拍误判成双击。
              const rect = e.currentTarget.getBoundingClientRect()
              const x = rect.width > 0 ? (e.clientX - rect.left) / rect.width : 0.5
              if (x >= 0.3 && x <= 0.7) armCenterDoubleTap()
            }}
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
                // 全屏：只在浏览器支持元素全屏时出现（iOS Safari 不支持，见上方注释）。
                ...(fullscreenSupported
                  ? [
                      {
                        icon: fullscreen ? <Minimize size={18} /> : <Maximize size={18} />,
                        label: fullscreen ? '还原' : '全屏',
                        action: toggleFullscreen,
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
