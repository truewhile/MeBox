import { create } from 'zustand'
import { persist } from 'zustand/middleware'

// 阅读主题（色值对齐 legado assets/defaultData/readConfig.json 的 6 套内置主题，
// 含日/夜两态，见 docs/reader-ui-spec.md 第 2/10 节）。
export interface ReaderTheme {
  id: string
  name: string
  bg: string
  text: string
  accent: string
  nightBg: string
  nightText: string
  nightAccent: string
}

export const READER_THEMES: ReaderTheme[] = [
  { id: 'weread', name: '微信读书', bg: '#C0EDC6', text: '#0B0B0B', accent: '#E53935', nightBg: '#000000', nightText: '#ADADAD', nightAccent: '#FE4D55' },
  { id: 'preset1', name: '预设1', bg: '#FFFFFF', text: '#000000', accent: '#E53935', nightBg: '#000000', nightText: '#FFFFFF', nightAccent: '#FE4D55' },
  { id: 'preset2', name: '羊皮纸', bg: '#DDC090', text: '#3E3422', accent: '#834E00', nightBg: '#3C3F43', nightText: '#DCDFE1', nightAccent: '#FE4D55' },
  { id: 'preset3', name: '护眼绿', bg: '#C2D8AA', text: '#596C44', accent: '#E53935', nightBg: '#3C3F43', nightText: '#88C16F', nightAccent: '#FE4D55' },
  { id: 'preset4', name: '粉紫', bg: '#DBB8E2', text: '#68516C', accent: '#801314', nightBg: '#3C3F43', nightText: '#F6AEAE', nightAccent: '#90BFF5' },
  { id: 'preset5', name: '淡蓝', bg: '#ABCEE0', text: '#3D4C54', accent: '#E53935', nightBg: '#3C3F43', nightText: '#90BFF5', nightAccent: '#FE4D55' },
]

export function getReaderTheme(themeId: string, night: boolean): { bg: string; text: string; accent: string } {
  const t = READER_THEMES.find((x) => x.id === themeId) ?? READER_THEMES[1]
  return night
    ? { bg: t.nightBg, text: t.nightText, accent: t.nightAccent }
    : { bg: t.bg, text: t.text, accent: t.accent }
}

export type ReaderPageMode = 'page' | 'scroll'
export type ReaderHomeMode = 'media' | 'reading'

/** 书架布局：网格封面 / 常规列表 / 紧凑列表（对应 legado AppConfig.bookshelfLayout）。 */
export type ReaderShelfLayout = 'grid' | 'list' | 'compact'

/**
 * 书架排序（对应 legado AppConfig 的 bookSort）：
 * recent=最近阅读、update=最近更新、name=书名、author=作者、mixed=综合、manual=手动顺序。
 */
export type ReaderShelfSort = 'recent' | 'update' | 'name' | 'author' | 'mixed' | 'manual'

/** 书架网格列数：0 表示按屏幕自适应（legado 是固定 2–6 列）。 */
export const SHELF_GRID_COLUMNS = [0, 2, 3, 4, 5, 6] as const

/**
 * 漫画图片的显示尺寸档位（上下滚动模式用）。
 *
 * legado 的漫画阅读有「缩放」，Web 版原先只有一种写死的宽度（正文列封顶 900px），
 * 桌面端看不到也放不大，所以补上这一组档位。屏幕越宽越该放大，属于设备级偏好，
 * 和 comicDoublePage 一样只存本机、不参与账号同步。
 *
 * - `default`  现状：正文列封顶 900px 居中，图片铺满该列
 * - `width`    适应宽度：正文列不再封顶，图片铺满窗口宽度
 * - `height`   适应高度：每页高度贴合一屏（一屏一页），宽度按原始比例
 * - `long`     适应长边：整页完整可见，宽高都不超出视口
 * - `original` 原始像素 1:1：既不放大也不缩小，超出部分横向滚动
 */
export type ComicImageFit = 'default' | 'width' | 'height' | 'long' | 'original'

/** 漫画图片尺寸档位的展示顺序与标签（界面面板按此渲染）。 */
export const COMIC_IMAGE_FITS: { id: ComicImageFit; label: string }[] = [
  { id: 'default', label: '默认' },
  { id: 'width', label: '适应宽度' },
  { id: 'height', label: '适应高度' },
  { id: 'long', label: '适应长边' },
  { id: 'original', label: '原图' },
]

interface ReaderSettingsState {
  // 首页模式切换（影视 / 阅读）
  homeMode: ReaderHomeMode
  setHomeMode: (mode: ReaderHomeMode) => void

  // ── 书架布局与展示（对应 legado 的书架设置）──
  /** 布局样式。 */
  shelfLayout: ReaderShelfLayout
  setShelfLayout: (layout: ReaderShelfLayout) => void
  /** 网格列数，0 为自适应。 */
  shelfGridColumns: number
  setShelfGridColumns: (columns: number) => void
  /** 排序方式。 */
  shelfSort: ReaderShelfSort
  setShelfSort: (sort: ReaderShelfSort) => void
  /** 是否显示未读章数徽标。 */
  shelfShowUnread: boolean
  setShelfShowUnread: (show: boolean) => void
  /** 是否显示「更新时间」一行（仅列表布局）。 */
  shelfShowUpdateTime: boolean
  setShelfShowUpdateTime: (show: boolean) => void

  themeId: string
  setThemeId: (id: string) => void

  night: boolean
  toggleNight: () => void
  setNight: (night: boolean) => void

  pageMode: ReaderPageMode
  setPageMode: (mode: ReaderPageMode) => void

  /** 漫画左右「双页铺开」偏好（legado 无对应项，是 Web 端桌面化补充）。 */
  comicDoublePage: boolean
  setComicDoublePage: (on: boolean) => void

  /** 漫画图片显示尺寸档位（设备级偏好，见 ComicImageFit）。 */
  comicImageFit: ComicImageFit
  setComicImageFit: (fit: ComicImageFit) => void

  // ── 搜索范围（对应 legado 搜索页的 SearchScopeDialog / AppConfig.searchScope）──
  /**
   * 已选书源 ID；空数组表示「全部启用书源」（默认）。只影响书籍搜索的并发范围。
   * 和 comicDoublePage 一样是设备级偏好：刻意不参与账号同步（legado 也把
   * searchScope 存在本机 AppConfig），换设备重新默认全选即可。
   */
  searchScopeIds: string[]
  setSearchScopeIds: (ids: string[]) => void

  fontSize: number
  setFontSize: (size: number) => void

  lineHeight: number
  setLineHeight: (lh: number) => void

  paragraphSpacing: number
  setParagraphSpacing: (v: number) => void

  // ── 听书（音频源播放器）偏好，对齐 legado AudioPlayService ──
  /** 播放倍速（AudioPlay.playSpeed），0.5–3.0，步进 0.1。 */
  audioSpeed: number
  setAudioSpeed: (v: number) => void
  /** 定时关闭默认分钟数（AppConfig.ttsTimer 语义），0 表示不定时。 */
  audioTimerMinutes: number
  setAudioTimerMinutes: (v: number) => void
}

/** 听书倍速可选值（legado 是 0.1 步进的浮点，这里收在 0.5–3.0）。 */
export const AUDIO_SPEEDS = [0.5, 0.75, 1, 1.25, 1.5, 1.75, 2, 2.5, 3]

/** 定时关闭预设分钟数（legado ReadAloudDialog times 数组）。 */
export const AUDIO_TIMERS = [0, 5, 10, 15, 30, 60, 90, 180]

/** 片头/片尾秒数上限（0 表示不跳过）。 */
export const AUDIO_CREDITS_MAX = 300

export const useReaderSettingsStore = create<ReaderSettingsState>()(
  persist(
    (set) => ({
      homeMode: 'media',
      setHomeMode: (mode) => set({ homeMode: mode }),

      shelfLayout: 'grid',
      setShelfLayout: (shelfLayout) => set({ shelfLayout }),

      shelfGridColumns: 0,
      setShelfGridColumns: (shelfGridColumns) =>
        set({ shelfGridColumns: Math.min(6, Math.max(0, Math.round(shelfGridColumns))) }),

      shelfSort: 'recent',
      setShelfSort: (shelfSort) => set({ shelfSort }),

      shelfShowUnread: true,
      setShelfShowUnread: (shelfShowUnread) => set({ shelfShowUnread }),

      shelfShowUpdateTime: true,
      setShelfShowUpdateTime: (shelfShowUpdateTime) => set({ shelfShowUpdateTime }),

      themeId: 'preset1',
      setThemeId: (themeId) => set({ themeId }),

      night: false,
      toggleNight: () => set((s) => ({ night: !s.night })),
      setNight: (night) => set({ night }),

      pageMode: 'page',
      setPageMode: (pageMode) => set({ pageMode }),

      // 默认开启：桌面端（窗口够宽）漫画自动两页铺开，窄屏（手机/窗口拉窄）
      // 由渲染层按窗口宽度自动退回单页，所以这里开着不会影响手机阅读。
      // 刻意不做跨设备同步：双页是否合适取决于屏幕宽度，属于设备级偏好，
      // 和 homeMode 同理（见 utils/readerSettingsSync.ts 的载荷字段）。
      comicDoublePage: true,
      setComicDoublePage: (comicDoublePage) => set({ comicDoublePage }),

      // 默认保持老样子（正文列 900px 居中）：放大到铺满屏幕是「想要更大」时才做的事，
      // 不该在升级后突然改变所有人已经习惯的宽度。想放大点「适应宽度」，想 1:1 看细节
      // 点「原图」。
      comicImageFit: 'default',
      setComicImageFit: (comicImageFit) => set({ comicImageFit }),

      searchScopeIds: [],
      setSearchScopeIds: (ids) => set({ searchScopeIds: [...new Set(ids)] }),

      fontSize: 20,
      setFontSize: (fontSize) => set({ fontSize: Math.min(32, Math.max(14, fontSize)) }),

      lineHeight: 1.8,
      setLineHeight: (lineHeight) => set({ lineHeight: Math.min(2.6, Math.max(1.4, Math.round(lineHeight * 10) / 10)) }),

      paragraphSpacing: 8,
      setParagraphSpacing: (paragraphSpacing) =>
        set({ paragraphSpacing: Math.min(32, Math.max(0, paragraphSpacing)) }),

      audioSpeed: 1,
      setAudioSpeed: (audioSpeed) =>
        set({ audioSpeed: Math.min(3, Math.max(0.5, Math.round(audioSpeed * 10) / 10)) }),

      audioTimerMinutes: 0,
      setAudioTimerMinutes: (audioTimerMinutes) =>
        set({ audioTimerMinutes: Math.min(180, Math.max(0, Math.round(audioTimerMinutes))) }),
    }),
    { name: 'mebox-reader-settings' },
  ),
)

/**
 * 与账号同步的阅读器偏好（对应后端 GET/PUT /reader/profile 的载荷）。
 *
 * 这些设置原先是设备级的（只存 localStorage）；现在按用户落库，换设备也能保持一致。
 * 首页的「影视 / 阅读」模式（homeMode）、漫画双页（comicDoublePage）与漫画图片尺寸
 * （comicImageFit）属于设备偏好，故意不参与同步——前者是入口选择，后两者是否合适
 * 取决于屏幕宽度和分辨率。
 */
export interface ReaderSettingsProfile {
  theme_id: string
  night: boolean
  page_mode: ReaderPageMode
  font_size: number
  line_height: number
  paragraph_spacing: number
  audio_speed: number
  audio_timer_minutes: number
  shelf_layout: ReaderShelfLayout
  shelf_grid_columns: number
  shelf_sort: ReaderShelfSort
  shelf_show_unread: boolean
  shelf_show_update_time: boolean
}

/** 把本地状态整理成 /reader/profile 的载荷（只含参与同步的字段）。 */
export function readerSettingsPayload(s: ReaderSettingsState): ReaderSettingsProfile {
  return {
    theme_id: s.themeId,
    night: s.night,
    page_mode: s.pageMode,
    font_size: s.fontSize,
    line_height: s.lineHeight,
    paragraph_spacing: s.paragraphSpacing,
    audio_speed: s.audioSpeed,
    audio_timer_minutes: s.audioTimerMinutes,
    shelf_layout: s.shelfLayout,
    shelf_grid_columns: s.shelfGridColumns,
    shelf_sort: s.shelfSort,
    shelf_show_unread: s.shelfShowUnread,
    shelf_show_update_time: s.shelfShowUpdateTime,
  }
}

/**
 * 用服务端的偏好覆盖本地状态（服务端是权威来源）。
 * 走 store 的 setter 而不是 setState，保证范围收敛只有一处实现。
 */
export function applyReaderSettingsProfile(p: ReaderSettingsProfile): void {
  const s = useReaderSettingsStore.getState()
  s.setThemeId(p.theme_id)
  s.setNight(p.night)
  s.setPageMode(p.page_mode)
  s.setFontSize(p.font_size)
  s.setLineHeight(p.line_height)
  s.setParagraphSpacing(p.paragraph_spacing)
  s.setAudioSpeed(p.audio_speed)
  s.setAudioTimerMinutes(p.audio_timer_minutes)
  s.setShelfLayout(p.shelf_layout)
  s.setShelfGridColumns(p.shelf_grid_columns)
  s.setShelfSort(p.shelf_sort)
  s.setShelfShowUnread(p.shelf_show_unread)
  s.setShelfShowUpdateTime(p.shelf_show_update_time)
}
