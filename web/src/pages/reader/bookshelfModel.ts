// 书架展示与排序的纯逻辑（不依赖 React，便于单测）。
//
// 语义对齐 legado 书架：未读章数取自 getUnreadChapterNum，「最近更新」取自
// Book.latestChapterTime，排序项与 AppConfig.getBookSortByGroupId 的六个取值一一对应。

import type { ReaderBook } from '../../api/reader'
import type { ReaderShelfLayout, ReaderShelfSort } from '../../stores/readerSettings'

/** 未读章数：total_chapter_num 为 0 表示目录尚未缓存，返回 null（前端不显示徽标）。 */
export function unreadChapters(book: ReaderBook): number | null {
  if (book.total_chapter_num <= 0) return null
  // dur_chapter_time 为 0 表示还没开始读，否则读到 dur_chapter_index 为止。
  const read = book.dur_chapter_time > 0 ? book.dur_chapter_index + 1 : 0
  return Math.max(0, Math.min(book.total_chapter_num, book.total_chapter_num - read))
}

/** 书籍的「最近更新」时间（毫秒），未检测到更新时为 0。 */
export function shelfUpdateTime(book: ReaderBook): number {
  return Number.isFinite(book.latest_chapter_time) ? book.latest_chapter_time : 0
}

function byTitle(a: ReaderBook, b: ReaderBook): number {
  return a.name.localeCompare(b.name, 'zh-Hans-CN')
}

function byAuthor(a: ReaderBook, b: ReaderBook): number {
  return (a.author || '').localeCompare(b.author || '', 'zh-Hans-CN') || byTitle(a, b)
}

/**
 * 按所选方式排序书架，不修改入参。
 *
 * - recent：最近阅读（dur_chapter_time 倒序，没读过的按书名排后面）——legado 默认
 * - update：最近更新（latest_chapter_time 倒序）
 * - name / author：书名 / 作者
 * - mixed：综合（阅读时间与更新时间取较新者）
 * - manual：手动顺序（服务端 order 字段）
 */
export function sortShelfBooks(books: ReaderBook[], sort: ReaderShelfSort): ReaderBook[] {
  const list = [...books]
  switch (sort) {
    case 'update':
      return list.sort(
        (a, b) => shelfUpdateTime(b) - shelfUpdateTime(a) || b.dur_chapter_time - a.dur_chapter_time || byTitle(a, b),
      )
    case 'name':
      return list.sort(byTitle)
    case 'author':
      return list.sort(byAuthor)
    case 'mixed':
      return list.sort(
        (a, b) =>
          Math.max(shelfUpdateTime(b), b.dur_chapter_time) - Math.max(shelfUpdateTime(a), a.dur_chapter_time) ||
          byTitle(a, b),
      )
    case 'manual':
      return list.sort((a, b) => a.order - b.order || byTitle(a, b))
    case 'recent':
    default:
      return list.sort((a, b) => b.dur_chapter_time - a.dur_chapter_time || byTitle(a, b))
  }
}

/** 绝对时间：当天只显示时分，同年显示月日，跨年带年份。 */
export function formatBookTime(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return ''
  const d = new Date(ms)
  const now = new Date()
  const pad = (n: number) => String(n).padStart(2, '0')
  if (d.toDateString() === now.toDateString()) return `${pad(d.getHours())}:${pad(d.getMinutes())}`
  if (d.getFullYear() === now.getFullYear()) return `${d.getMonth() + 1}月${d.getDate()}日`
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

/** 相对时间：刚刚 / N 分钟前 / N 小时前 / N 天前，超过 30 天退回日期。 */
export function formatRelativeTime(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return ''
  const diff = Date.now() - ms
  if (diff < 60_000) return '刚刚'
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)} 分钟前`
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)} 小时前`
  if (diff < 30 * 86_400_000) return `${Math.floor(diff / 86_400_000)} 天前`
  return formatBookTime(ms)
}

/** 「读到」文案；未开始时返回空串。 */
export function readProgressText(book: ReaderBook): string {
  if (book.dur_chapter_time <= 0 || !book.dur_chapter_title) return ''
  return book.dur_chapter_title
}

export const SHELF_LAYOUT_OPTIONS: { value: ReaderShelfLayout; label: string; hint: string }[] = [
  { value: 'grid', label: '网格', hint: '封面宫格，可调列数' },
  { value: 'list', label: '列表', hint: '封面 + 书名 + 读到 / 最新章节' },
  { value: 'compact', label: '紧凑列表', hint: '小封面单行，一屏放更多书' },
]

export const SHELF_SORT_OPTIONS: { value: ReaderShelfSort; label: string }[] = [
  { value: 'recent', label: '最近阅读' },
  { value: 'update', label: '最近更新' },
  { value: 'mixed', label: '综合' },
  { value: 'name', label: '按书名' },
  { value: 'author', label: '按作者' },
  { value: 'manual', label: '手动顺序' },
]

/** 网格列数选项的展示文案。 */
export function gridColumnsLabel(columns: number): string {
  return columns === 0 ? '自适应' : `${columns} 列`
}

/** 书籍详情页地址（/reader/book），与搜索结果的跳转参数保持一致。 */
export function bookDetailPath(book: ReaderBook): string {
  const params = new URLSearchParams({
    source_url: book.origin,
    book_url: book.book_url,
    name: book.name,
    author: book.author || '',
    cover_url: book.cover_url || '',
    origin_name: book.origin_name || '',
  })
  return `/reader/book?${params.toString()}`
}
