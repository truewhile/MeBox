import { LONG_REQUEST_TIMEOUT, api } from './client'

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
  total_chapter_num: number
  dur_chapter_index: number
  dur_chapter_pos: number
  dur_chapter_title: string
  dur_chapter_time: number
  order: number
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

const longOpts = { timeout: LONG_REQUEST_TIMEOUT } as const

export const readerAPI = {
  // ── 书源 ──
  listSources: () => api.get<{ sources: ReaderSource[] }>('/reader/sources').then((r) => r.data.sources),
  importSources: (text: string) =>
    api.post<{ imported: number }>('/reader/sources/import', { text }, longOpts).then((r) => r.data.imported),
  setSourceEnabled: (id: string, enabled: boolean) => api.patch(`/reader/sources/${id}`, { enabled }),
  deleteSource: (id: string) => api.delete(`/reader/sources/${id}`),
  debugSource: (id: string, key: string) =>
    api.post<{ logs: string[] }>(`/reader/sources/${id}/debug`, { key }, longOpts).then((r) => r.data.logs),

  // ── 搜索 / 详情 / 目录 / 正文 ──
  search: (key: string) =>
    api
      .post<{ books: ReaderSearchBook[]; skipped: ReaderSearchSkipped[] }>('/reader/search', { key }, longOpts)
      .then((r) => r.data),
  bookInfo: (params: { source_id?: string; source_url?: string; book_url: string }) =>
    api.get<ReaderBookInfo>('/reader/book-info', { params, timeout: LONG_REQUEST_TIMEOUT }).then((r) => r.data),
  toc: (params: { source_id?: string; source_url?: string; book_url: string; toc_url: string }) =>
    api
      .get<{ chapters: ReaderTocChapter[] }>('/reader/toc', { params, timeout: LONG_REQUEST_TIMEOUT })
      .then((r) => r.data.chapters),
  content: (params: { source_id?: string; source_url?: string; book_url: string; chapter_url: string }) =>
    api
      .get<ReaderChapterContent>('/reader/content', { params, timeout: LONG_REQUEST_TIMEOUT })
      .then((r) => r.data),

  // ── 书架 ──
  listBooks: () => api.get<{ books: ReaderBook[] }>('/reader/books').then((r) => r.data.books),
  addBook: (body: { origin: ReaderSearchOrigin; name: string; author: string; cover_url: string }) =>
    api.post<ReaderBook>('/reader/books', body).then((r) => r.data),
  removeBook: (id: string) => api.delete(`/reader/books/${id}`),
  saveProgress: (id: string, body: { chapter_index: number; pos: number; chapter_title: string }) =>
    api.put(`/reader/books/${id}/progress`, body),
  listChapters: (id: string) =>
    api.get<{ chapters: ReaderChapter[] }>(`/reader/books/${id}/chapters`).then((r) => r.data.chapters),
  saveChapters: (id: string, chapters: ReaderChapter[]) => api.post(`/reader/books/${id}/chapters`, { chapters }),

  // ── 替换规则 ──
  listReplaceRules: () =>
    api.get<{ rules: ReaderReplaceRule[] }>('/reader/replace-rules').then((r) => r.data.rules),
}
