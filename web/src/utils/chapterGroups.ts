// 超长目录的「按段分组」纯逻辑。
//
// 起因：一本书动辄上千章（如 1317 章），目录面板只能一屏十来个条目地滑，
// 想跳到中后段只能长按滚动条反复拖。这里把条目按固定大小切成若干组，
// 面板顶部给出一个区间下拉（1-100 / 101-200 ...），选中即整段跳转。

/** 每组条目数。100 章一屏下拉里可读，长度也够短。 */
export const CHAPTER_GROUP_SIZE = 100

export type ChapterGroup = {
  /** 组序号，0 起。 */
  index: number
  /** 组内起始条目下标（含）。 */
  start: number
  /** 组内结束条目下标（含）。 */
  end: number
  /** 下拉展示用区间文案，如 "1-100"；末组不足 100 时自然变短（"1301-1317"）。 */
  label: string
}

/** 把 total 个条目按 size 切成若干组；total 非正数时返回空数组。 */
export function buildChapterGroups(total: number, size: number = CHAPTER_GROUP_SIZE): ChapterGroup[] {
  if (!Number.isFinite(total) || total <= 0) return []
  if (!Number.isFinite(size) || size <= 0) return []
  const count = Math.ceil(total / size)
  const groups: ChapterGroup[] = []
  for (let i = 0; i < count; i += 1) {
    const start = i * size
    const end = Math.min(total - 1, start + size - 1)
    groups.push({ index: i, start, end, label: `${start + 1}-${end + 1}` })
  }
  return groups
}

/**
 * 条目下标落在第几组。用于两处：
 * 打开面板时定位到当前章节所在组；滚动时把下拉显示同步到可见区间。
 * 越界（负数 / 超过总数）一律夹到有效范围内，避免下拉出现空值。
 */
export function chapterGroupIndexOf(
  itemIndex: number,
  total: number,
  size: number = CHAPTER_GROUP_SIZE,
): number {
  const groupCount = Math.ceil(Math.max(0, total) / size)
  if (groupCount <= 0) return 0
  const raw = Math.floor((Number.isFinite(itemIndex) ? itemIndex : 0) / size)
  return Math.min(Math.max(raw, 0), groupCount - 1)
}
