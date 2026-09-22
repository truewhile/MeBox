// 媒体库筛选条件：解析/序列化为 URL 查询串。
//
// 放在 URL 而不是组件状态里，是为了让「筛选后的库」可以直接分享、刷新后保持、
// 以及用浏览器返回键撤销上一步筛选。

export type LibraryFilterParams = {
  genres: string[]
  year_min?: number
  year_max?: number
  rating_min?: number
  unwatched?: boolean
}

export const EMPTY_LIBRARY_FILTERS: LibraryFilterParams = { genres: [] }

const GENRE_PARAM = 'genre'
const YEAR_MIN_PARAM = 'year_min'
const YEAR_MAX_PARAM = 'year_max'
const RATING_MIN_PARAM = 'rating_min'
const UNWATCHED_PARAM = 'unwatched'

/** 判断是否有任何筛选生效（用于显示「清除筛选」与空态文案）。 */
export function hasActiveFilters(filters: LibraryFilterParams): boolean {
  return (
    filters.genres.length > 0 ||
    Boolean(filters.year_min) ||
    Boolean(filters.year_max) ||
    Boolean(filters.rating_min) ||
    Boolean(filters.unwatched)
  )
}

export function parseLibraryFilters(search: string): LibraryFilterParams {
  const params = new URLSearchParams(search.startsWith('?') ? search.slice(1) : search)
  const filters: LibraryFilterParams = {
    genres: params.getAll(GENRE_PARAM).filter((value) => value.trim() !== ''),
  }
  const yearMin = Number(params.get(YEAR_MIN_PARAM))
  if (Number.isFinite(yearMin) && yearMin > 0) filters.year_min = yearMin
  const yearMax = Number(params.get(YEAR_MAX_PARAM))
  if (Number.isFinite(yearMax) && yearMax > 0) filters.year_max = yearMax
  const ratingMin = Number(params.get(RATING_MIN_PARAM))
  if (Number.isFinite(ratingMin) && ratingMin > 0) filters.rating_min = ratingMin
  if (isTruthy(params.get(UNWATCHED_PARAM))) filters.unwatched = true
  return filters
}

/**
 * serializeLibraryFilters 生成筛选参数的查询串（不含前导 `?`，也不含排序等
 * 其它参数）。空筛选返回空串。
 */
export function serializeLibraryFilters(filters: LibraryFilterParams): string {
  const params = new URLSearchParams()
  filters.genres.forEach((genre) => {
    const trimmed = genre.trim()
    if (trimmed) params.append(GENRE_PARAM, trimmed)
  })
  if (filters.year_min) params.set(YEAR_MIN_PARAM, String(filters.year_min))
  if (filters.year_max) params.set(YEAR_MAX_PARAM, String(filters.year_max))
  if (filters.rating_min) params.set(RATING_MIN_PARAM, String(filters.rating_min))
  if (filters.unwatched) params.set(UNWATCHED_PARAM, '1')
  return params.toString()
}

/**
 * toFilterQuery 把筛选条件转成 axios params，供列表与随机接口共用。
 * 键名与服务端解析保持一致。
 */
export function toFilterQuery(filters: LibraryFilterParams): Record<string, unknown> {
  const query: Record<string, unknown> = {}
  if (filters.genres.length > 0) query[GENRE_PARAM] = filters.genres
  if (filters.year_min) query[YEAR_MIN_PARAM] = filters.year_min
  if (filters.year_max) query[YEAR_MAX_PARAM] = filters.year_max
  if (filters.rating_min) query[RATING_MIN_PARAM] = filters.rating_min
  if (filters.unwatched) query[UNWATCHED_PARAM] = 1
  return query
}

/**
 * withFilterParams 在原查询串上叠加筛选参数，同时保留其它参数（如排序记忆）。
 */
export function withFilterParams(search: string, filters: LibraryFilterParams): string {
  const params = new URLSearchParams(search.startsWith('?') ? search.slice(1) : search)
  params.delete(GENRE_PARAM)
  params.delete(YEAR_MIN_PARAM)
  params.delete(YEAR_MAX_PARAM)
  params.delete(RATING_MIN_PARAM)
  params.delete(UNWATCHED_PARAM)
  filters.genres.forEach((genre) => {
    const trimmed = genre.trim()
    if (trimmed) params.append(GENRE_PARAM, trimmed)
  })
  if (filters.year_min) params.set(YEAR_MIN_PARAM, String(filters.year_min))
  if (filters.year_max) params.set(YEAR_MAX_PARAM, String(filters.year_max))
  if (filters.rating_min) params.set(RATING_MIN_PARAM, String(filters.rating_min))
  if (filters.unwatched) params.set(UNWATCHED_PARAM, '1')
  const query = params.toString()
  return query ? `?${query}` : ''
}

/**
 * normalizeYearRange 修正用户输入的年份区间：上限早于下限时交换，
 * 避免一次无心的顺序颠倒把结果筛成空。
 */
export function normalizeYearRange(
  min?: number,
  max?: number,
): { year_min?: number; year_max?: number } {
  if (min && max && min > max) {
    return { year_min: max, year_max: min }
  }
  return { year_min: min, year_max: max }
}

function isTruthy(value: string | null): boolean {
  if (!value) return false
  const normalized = value.toLowerCase()
  return normalized === '1' || normalized === 'true' || normalized === 'yes' || normalized === 'on'
}
