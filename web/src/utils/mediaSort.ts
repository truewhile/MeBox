import type { Media } from '../types'
import { seriesTitle, type SeriesCard } from './groupSeries'

export type SortField =
  | 'title'
  | 'release_date'
  | 'year'
  | 'created_at'
  | 'updated_at'
  | 'rating'
  | 'imdb_rating'
  | 'last_played'
  | 'duration'
  | 'bitrate'
  | 'random'

export type SortOrder = 'asc' | 'desc'

export type SortOption = {
  id: SortField
  label: string
  defaultOrder: SortOrder
}

export const SORT_OPTIONS: SortOption[] = [
  { id: 'title', label: '标题', defaultOrder: 'asc' },
  { id: 'release_date', label: '发行日期', defaultOrder: 'desc' },
  { id: 'year', label: '年份', defaultOrder: 'desc' },
  { id: 'created_at', label: '加入日期', defaultOrder: 'desc' },
  { id: 'updated_at', label: '最后一集添加日期', defaultOrder: 'desc' },
  { id: 'rating', label: '影评人评分', defaultOrder: 'desc' },
  { id: 'imdb_rating', label: 'IMDB评分', defaultOrder: 'desc' },
  { id: 'last_played', label: '播放日期', defaultOrder: 'desc' },
  { id: 'duration', label: '播放时长', defaultOrder: 'desc' },
  { id: 'bitrate', label: '比特率', defaultOrder: 'desc' },
  { id: 'random', label: '随机', defaultOrder: 'desc' },
]

export function getSortOption(field: SortField): SortOption {
  return SORT_OPTIONS.find((opt) => opt.id === field) ?? SORT_OPTIONS[0]
}

// 简单伪随机洗牌（基于 seed 保证同一渲染周期内稳定）
function pseudoRandomShuffle<T>(array: T[], seed = 1): T[] {
  const result = [...array]
  let currentSeed = seed
  const random = () => {
    currentSeed = (currentSeed * 9301 + 49297) % 233280
    return currentSeed / 233280
  }
  for (let i = result.length - 1; i > 0; i--) {
    const j = Math.floor(random() * (i + 1))
    const temp = result[i]
    result[i] = result[j]
    result[j] = temp
  }
  return result
}

function compareStrings(a?: string, b?: string, order: SortOrder = 'asc'): number {
  const valA = (a ?? '').trim()
  const valB = (b ?? '').trim()
  if (!valA && !valB) return 0
  if (!valA) return 1
  if (!valB) return -1
  const cmp = valA.localeCompare(valB, 'zh-CN', { numeric: true, sensitivity: 'base' })
  return order === 'asc' ? cmp : -cmp
}

function compareNumbers(a: number, b: number, order: SortOrder = 'asc'): number {
  if (isNaN(a) && isNaN(b)) return 0
  if (isNaN(a)) return 1
  if (isNaN(b)) return -1
  return order === 'asc' ? a - b : b - a
}

function compareDates(a?: string, b?: string, order: SortOrder = 'asc'): number {
  const timeA = a ? new Date(a).getTime() : 0
  const timeB = b ? new Date(b).getTime() : 0
  const safeTimeA = isNaN(timeA) ? 0 : timeA
  const safeTimeB = isNaN(timeB) ? 0 : timeB
  if (!safeTimeA && !safeTimeB) return 0
  if (!safeTimeA) return 1
  if (!safeTimeB) return -1
  return order === 'asc' ? safeTimeA - safeTimeB : safeTimeB - safeTimeA
}

export function sortMediaList(
  items: Media[],
  field: SortField,
  order: SortOrder,
  randomSeed = 1,
  historyMap?: Map<string, string>,
): Media[] {
  if (!items || items.length === 0) return []
  if (field === 'random') {
    return pseudoRandomShuffle(items, randomSeed)
  }

  const list = [...items]
  list.sort((a, b) => {
    switch (field) {
      case 'title':
        return compareStrings(a.title || a.original_name, b.title || b.original_name, order)
      case 'release_date': {
        const dateA = a.release_date || (a.year ? `${a.year}-01-01` : '')
        const dateB = b.release_date || (b.year ? `${b.year}-01-01` : '')
        return compareDates(dateA, dateB, order) || compareStrings(a.title, b.title, 'asc')
      }
      case 'year': {
        const cmp = compareNumbers(a.year || 0, b.year || 0, order)
        return cmp !== 0 ? cmp : compareStrings(a.title, b.title, 'asc')
      }
      case 'created_at':
        return compareDates(a.created_at, b.created_at, order) || compareStrings(a.title, b.title, 'asc')
      case 'updated_at':
        return compareDates(a.updated_at || a.created_at, b.updated_at || b.created_at, order) || compareStrings(a.title, b.title, 'asc')
      case 'rating':
      case 'imdb_rating': {
        const cmp = compareNumbers(a.rating || 0, b.rating || 0, order)
        return cmp !== 0 ? cmp : compareStrings(a.title, b.title, 'asc')
      }
      case 'last_played': {
        const playedA = historyMap?.get(a.id)
        const playedB = historyMap?.get(b.id)
        return compareDates(playedA, playedB, order) || compareStrings(a.title, b.title, 'asc')
      }
      case 'duration': {
        const cmp = compareNumbers(a.duration_sec || 0, b.duration_sec || 0, order)
        return cmp !== 0 ? cmp : compareStrings(a.title, b.title, 'asc')
      }
      case 'bitrate': {
        // 比特率 / 大小
        const cmp = compareNumbers(a.size_bytes || 0, b.size_bytes || 0, order)
        return cmp !== 0 ? cmp : compareStrings(a.title, b.title, 'asc')
      }
      default:
        return 0
    }
  })
  return list
}

export function sortSeriesList(
  cards: SeriesCard[],
  field: SortField,
  order: SortOrder,
  randomSeed = 1,
  historyMap?: Map<string, string>,
): SeriesCard[] {
  if (!cards || cards.length === 0) return []
  if (field === 'random') {
    return pseudoRandomShuffle(cards, randomSeed)
  }

  const list = [...cards]
  list.sort((a, b) => {
    const repA = a.rep
    const repB = b.rep
    const titleA = seriesTitle(repA)
    const titleB = seriesTitle(repB)

    switch (field) {
      case 'title':
        return compareStrings(titleA, titleB, order)
      case 'release_date': {
        const dateA = repA.release_date || (repA.year ? `${repA.year}-01-01` : '')
        const dateB = repB.release_date || (repB.year ? `${repB.year}-01-01` : '')
        return compareDates(dateA, dateB, order) || compareStrings(titleA, titleB, 'asc')
      }
      case 'year': {
        const cmp = compareNumbers(repA.year || 0, repB.year || 0, order)
        return cmp !== 0 ? cmp : compareStrings(titleA, titleB, 'asc')
      }
      case 'created_at':
        return compareDates(repA.created_at, repB.created_at, order) || compareStrings(titleA, titleB, 'asc')
      case 'updated_at': {
        // 远程挂载库的卡片由服务器按 DateLastContentAdded（上次添加集日期）
        // 倒序返回且不回传日期值：缺失时保持服务器顺序，避免错误回退成加入日期。
        // 本地库卡片（groupSeries）始终带 last_added_at，按真实值排序。
        if (!a.last_added_at && !b.last_added_at) return 0
        const updateA = a.last_added_at || ''
        const updateB = b.last_added_at || ''
        return compareDates(updateA, updateB, order) || compareStrings(titleA, titleB, 'asc')
      }
      case 'rating':
      case 'imdb_rating': {
        const cmp = compareNumbers(repA.rating || 0, repB.rating || 0, order)
        return cmp !== 0 ? cmp : compareStrings(titleA, titleB, 'asc')
      }
      case 'last_played': {
        const playedA = historyMap?.get(repA.id)
        const playedB = historyMap?.get(repB.id)
        return compareDates(playedA, playedB, order) || compareStrings(titleA, titleB, 'asc')
      }
      case 'duration': {
        const cmp = compareNumbers(repA.duration_sec || 0, repB.duration_sec || 0, order)
        return cmp !== 0 ? cmp : compareStrings(titleA, titleB, 'asc')
      }
      case 'bitrate': {
        const cmp = compareNumbers(repA.size_bytes || 0, repB.size_bytes || 0, order)
        return cmp !== 0 ? cmp : compareStrings(titleA, titleB, 'asc')
      }
      default:
        return 0
    }
  })
  return list
}
