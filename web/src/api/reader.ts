import { LONG_REQUEST_TIMEOUT, api } from './client'
// 只引类型：阅读器偏好的字段定义在 store（它是本地状态的形状），
// 这里作为 /reader/profile 的线上载荷复用，类型导入不会产生运行时依赖。
import type { ReaderSettingsProfile } from '../stores/readerSettings'

// 阅读子系统 API（/api/reader/*），字段与后端 model/reader.go 对齐。

export interface ReaderSearchOrigin {
  source_id: string
  origin: string
  origin_name: string
  origin_type: number
  book_url: string
  latest_chapter: string
}

export interface ReaderSearchBook {
  name: string
  author: string
  kind: string
  word_count: string
  latest_chapter: string
  intro: string
  cover_url: string
  book_url: string
  origins: ReaderSearchOrigin[]
}

export interface ReaderSearchSkipped {
  source_id: string
  origin_name: string
  reason: string
}

export interface ReaderSource {
  id: string
  name: string
  group: string
  type: number
  source_url: string
  enabled: boolean
  enabled_explore: boolean
  custom_order: number
  weight: number
  comment: string
  last_update_time: number
  respond_time: number
  // 书源声明了登录能力（loginUrl / loginUi）
  has_login: boolean
}

export interface ReaderBook {
  id: string
  origin: string
  origin_name: string
  book_url: string
  toc_url: string
  name: string
  author: string
  kind: string
  cover_url: string
  intro: string
  type: number
  latest_chapter_title: string
  /** 最后一次检测到「目录末尾章节变化」的时间（毫秒，0 表示未检测到更新）。 */
  latest_chapter_time: number
  total_chapter_num: number
  dur_chapter_index: number
  dur_chapter_pos: number
  dur_chapter_title: string
  dur_chapter_time: number
  order: number
  /** 本地导入书籍（TXT / EPUB / 有声书目录），正文在服务端，不走书源。 */
  is_local: boolean
  /** 原地引用服务器上已有的文件/目录：移出书架只解除引用，不删除源文件。 */
  local_external: boolean
  /** 听书跳过片头/片尾秒数（0 表示不跳过，对应 legado Book.openCredits/closeCredits）。 */
  open_credits: number
  close_credits: number
}

/** 书架分组（对齐影视模块的媒体库标签）：组名 + 组内书籍 ID，顺序即组内展示顺序。 */
export interface BookGroup {
  name: string
  book_ids: string[]
}

export interface ReaderBookInfo {
  name: string
  author: string
  kind: string
  word_count: string
  latest_chapter: string
  intro: string
  cover_url: string
  toc_url: string
  book_url: string
}

export interface ReaderTocChapter {
  index: number
  title: string
  url: string
  is_volume: boolean
  update_time: string
}

export interface ReaderChapter {
  index: number
  title: string
  url: string
  is_volume: boolean
}

export interface ReaderChapterContent {
  type: 'text' | 'audio' | 'image'
  content?: string
  tracks?: string[]
  images?: string[]
  image_style?: string
  /** 该音轨走了服务端转码（源格式浏览器解不了），首次播放需要等转码完成。 */
  transcoding?: boolean
}

export interface ReaderReplaceRule {
  id: string
  name: string
  group: string
  pattern: string
  replacement: string
  scope: string
  scope_title: boolean
  scope_content: boolean
  exclude_scope: string
  is_enabled: boolean
  is_regex: boolean
  order: number
}

// ── 书源登录 ──

export interface ReaderLoginField {
  name: string
  type: 'text' | 'password' | 'button' | 'toggle' | 'select'
  action?: string
  chars?: string[]
  default?: string
  viewName?: string
  style?: Record<string, unknown>
}

export interface ReaderSourceLogin {
  source_id: string
  source_name: string
  has_login_js: boolean
  login_js?: string
  fields: ReaderLoginField[]
  values: Record<string, string>
  cookies: Record<string, string>
  variable: string
  variable_comment?: string
  logged_in: boolean
  error?: string
}

export interface ReaderBrowserRequest {
  url: string
  title: string
}

/** 书源 JS 交给宿主浏览器承载的一个页面（java.startBrowser / startBrowserAwait）。 */
export interface ReaderBrowserPage {
  id: string
  title: string
  /** wait = 需要回传 DOM（点 √）；open = 仅展示。 */
  mode: 'wait' | 'open'
  /** iframe 承载地址（同源，带签名）。 */
  page_url: string
  refetch: boolean
  source_id: string
  target_url?: string
}

export interface ReaderLoginResult {
  ok: boolean
  error?: string
  toasts?: string[]
  browsers?: ReaderBrowserRequest[]
  /** 书源要求重新渲染登录表单（java.reLoginView / refreshExplore / upLoginData）。 */
  ui_refresh?: boolean
  values: Record<string, string>
  cookies: Record<string, string>
  logged_in: boolean
}

const longOpts = { timeout: LONG_REQUEST_TIMEOUT } as const

// 登录动作可能阻塞等待用户在页面里操作（java.startBrowserAwait），
// 服务端上限 10 分钟，这里留出余量，避免 axios 先超时把请求掐掉。
const browserWaitOpts = { timeout: 11 * 60_000 } as const

export const readerAPI = {
  // ── 书源 ──
  listSources: () => api.get<{ sources: ReaderSource[] }>('/reader/sources').then((r) => r.data.sources),
  importSources: (text: string) =>
    api.post<{ imported: number }>('/reader/sources/import', { text }, longOpts).then((r) => r.data.imported),
  setSourceEnabled: (id: string, enabled: boolean) => api.patch(`/reader/sources/${id}`, { enabled }),
  deleteSource: (id: string) => api.delete(`/reader/sources/${id}`),
  debugSource: (id: string, key: string) =>
    api.post<{ logs: string[] }>(`/reader/sources/${id}/debug`, { key }, longOpts).then((r) => r.data.logs),

  // ── 书源登录与源变量 ──
  // 取登录界面描述（loginUi 控件 + 已保存值 + 当前 Cookie 状态）
  sourceLogin: (id: string) => api.get<ReaderSourceLogin>(`/reader/sources/${id}/login`).then((r) => r.data),
  // 执行登录动作：action 为空表示执行 loginUrl 的 login()（确认登录）
  runSourceLogin: (id: string, body: { action?: string; fields?: Record<string, string> }) =>
    api.post<ReaderLoginResult>(`/reader/sources/${id}/login`, body, browserWaitOpts).then((r) => r.data),
  // 书源 JS 的宿主浏览器：轮询待用户完成的页面（startBrowserAwait 会阻塞在服务端）
  browserPending: (sourceId: string) =>
    api
      .get<{ pages: ReaderBrowserPage[] }>('/reader/browser/pending', { params: { source_id: sourceId } })
      .then((r) => r.data.pages ?? []),
  // 回传用户操作后的 DOM（或取消），解除服务端阻塞
  submitBrowserResult: (body: { id: string; body?: string; url?: string; cancelled?: boolean }) =>
    api.post('/reader/browser/result', body).then((r) => r.data),
  // 承载页面内的接口请求转交服务端代发（iframe 是不透明源，带不上书源 Cookie）
  browserXHR: (body: {
    id: string
    url: string
    method?: string
    headers?: Record<string, string>
    body?: string
  }) =>
    api
      .post<{ status: number; content_type: string; body: string; base64: boolean }>(
        '/reader/browser/xhr',
        body,
        longOpts,
      )
      .then((r) => r.data),
  // 仅保存表单值，不触发登录
  saveSourceLoginInfo: (id: string, fields: Record<string, string>) =>
    api.put(`/reader/sources/${id}/login-info`, { fields }),
  logoutSource: (id: string) => api.delete(`/reader/sources/${id}/login`),
  setSourceVariable: (id: string, variable: string) => api.put(`/reader/sources/${id}/variable`, { variable }),

  // ── 搜索 / 详情 / 目录 / 正文 ──
  // 多源聚合搜索。sourceIds 为空表示搜索范围＝全部启用书源（默认）；
  // 非空则只搜这些书源（后端会忽略其中已删除/停用的，全部失效时退回全部启用）。
  search: (key: string, sourceIds: string[] = []) =>
    api
      .post<{ books: ReaderSearchBook[] | null; skipped: ReaderSearchSkipped[] | null }>(
        '/reader/search',
        { key, source_ids: sourceIds },
        longOpts,
      )
      .then((r) => r.data),
  bookInfo: (params: { source_id?: string; source_url?: string; book_url: string }) =>
    api.get<ReaderBookInfo>('/reader/book-info', { params, timeout: LONG_REQUEST_TIMEOUT }).then((r) => r.data),
  toc: (params: { source_id?: string; source_url?: string; book_url: string; toc_url: string }) =>
    api
      .get<{ chapters: ReaderTocChapter[] | null }>('/reader/toc', { params, timeout: LONG_REQUEST_TIMEOUT })
      // 后端把 nil 目录编码成 null；调用方一律按数组处理，
      // 否则 `chapters.length` 会抛 TypeError（页面只剩一句 JS 报错）。
      .then((r) => r.data.chapters ?? []),
  content: (params: { source_id?: string; source_url?: string; book_url: string; chapter_url: string }) =>
    api
      .get<ReaderChapterContent>('/reader/content', { params, timeout: LONG_REQUEST_TIMEOUT })
      .then((r) => r.data),

  // ── 书架 ──
  listBooks: () => api.get<{ books: ReaderBook[] | null }>('/reader/books').then((r) => r.data.books ?? []),
  addBook: (body: { origin: ReaderSearchOrigin; name: string; author: string; cover_url: string }) =>
    api.post<ReaderBook>('/reader/books', body).then((r) => r.data),
  /** 上传本地书籍（TXT / EPUB），服务端解析目录后加入书架。 */
  uploadLocalBook: (file: File, onProgress?: (percent: number) => void) =>
    api
      .post<ReaderBook>('/reader/local/books', (() => {
        const form = new FormData()
        form.append('file', file)
        return form
      })(), {
        timeout: 0,
        onUploadProgress: (e) => {
          if (!onProgress || !e.total) return
          onProgress(Math.round((e.loaded / e.total) * 100))
        },
      })
      .then((r) => r.data),
  /** 从服务器已有文件导入书籍（TXT / EPUB），原地引用不复制。仅管理员。 */
  importLocalBookFromPath: (path: string) =>
    api
      .post<ReaderBook>('/reader/local/books/from-path', { path }, { timeout: LONG_REQUEST_TIMEOUT })
      .then((r) => r.data),
  /** 把服务器上的一本目录导入为有声书（音频文件 + .strm 播放指针）。仅管理员。 */
  importLocalAudioDir: (path: string) =>
    api
      .post<ReaderBook>('/reader/local/audiobooks', { path }, { timeout: LONG_REQUEST_TIMEOUT })
      .then((r) => r.data),
  removeBook: (id: string) => api.delete(`/reader/books/${id}`),
  /**
   * 读取当前账号的阅读器偏好（主题 / 排版 / 听书 / 书架展示）。
   * 从未保存过时返回 null，由前端用本地的值播种。
   */
  getReaderSettings: () =>
    api.get<{ profile: ReaderSettingsProfile | null }>('/reader/profile').then((r) => r.data.profile),
  /** 覆盖保存阅读器偏好（服务端会做范围收敛）。 */
  saveReaderSettings: (body: ReaderSettingsProfile) =>
    api.put<{ profile: ReaderSettingsProfile }>('/reader/profile', body).then((r) => r.data.profile),

  // ── 书架分组（每个用户一份，仿影视模块的媒体库标签）──
  /** 读取当前账号的书架分组；没有分组时返回空数组。 */
  getBookGroups: () =>
    api.get<{ groups: BookGroup[] | null }>('/reader/book-groups').then((r) => r.data.groups ?? []),
  /** 覆盖保存书架分组：服务端会收敛成「一书一组」并丢弃已不在书架上的书。 */
  setBookGroups: (groups: BookGroup[]) =>
    api.put<{ groups: BookGroup[] | null }>('/reader/book-groups', { groups }).then((r) => r.data.groups ?? []),
  /**
   * 更新目录（对应 legado 书架的「更新目录」）：重抓书架里全部网络书籍的目录，
   * 覆盖章节缓存并刷新「最近更新」时间。返回本次刷新的汇总：
   * updated 有新章节 / unchanged 抓成功但没新章节 / failed 抓取或写入失败。
   */
  refreshBooksToc: () =>
    api
      .post<{ total: number; updated: number; unchanged: number; failed: number }>(
        '/reader/shelf/refresh-toc',
        {},
        longOpts,
      )
      .then((r) => r.data),
  /**
   * 换源：把书架里的书切到另一个书源。
   * 阅读进度保留，旧源目录缓存由服务端清空并按新源重新预热。
   */
  switchOrigin: (id: string, origin: ReaderSearchOrigin) =>
    api.post<ReaderBook>(`/reader/books/${id}/origin`, { origin }, longOpts).then((r) => r.data),
  saveProgress: (id: string, body: { chapter_index: number; pos: number; chapter_title: string }) =>
    api.put(`/reader/books/${id}/progress`, body),
  // 听书跳过片头/片尾（秒，0 不跳过）
  saveAudioConfig: (id: string, body: { open_credits: number; close_credits: number }) =>
    api.put(`/reader/books/${id}/audio-config`, body),
  listChapters: (id: string) =>
    api
      .get<{ chapters: ReaderChapter[] | null }>(`/reader/books/${id}/chapters`)
      .then((r) => r.data.chapters ?? []),
  saveChapters: (id: string, chapters: ReaderChapter[]) => api.post(`/reader/books/${id}/chapters`, { chapters }),
  // 书架维度正文（服务端已应用书源 replaceRegex 与用户替换净化规则）
  bookContent: (id: string, chapter: number) =>
    api
      .get<ReaderChapterContent>(`/reader/books/${id}/content`, { params: { chapter }, timeout: LONG_REQUEST_TIMEOUT })
      .then((r) => r.data),

  // ── 替换净化规则 ──
  listReplaceRules: () =>
    api.get<{ rules: ReaderReplaceRule[] }>('/reader/replace-rules').then((r) => r.data.rules),
  createReplaceRule: (body: ReplaceRuleInput) =>
    api.post<ReaderReplaceRule>('/reader/replace-rules', body).then((r) => r.data),
  updateReplaceRule: (id: string, body: ReplaceRuleInput) => api.patch(`/reader/replace-rules/${id}`, body),
  deleteReplaceRule: (id: string) => api.delete(`/reader/replace-rules/${id}`),
}

export interface ReplaceRuleInput {
  name: string
  group: string
  pattern: string
  replacement: string
  scope: string
  scope_title: boolean
  scope_content: boolean
  exclude_scope: string
  is_enabled: boolean
  is_regex: boolean
  timeout_millisecond: number
  order: number
}
