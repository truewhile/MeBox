import type { ReaderSearchBook, ReaderSearchOrigin, ReaderSearchSkipped } from '../api/reader'

/**
 * 同名同作者的聚合键——与后端 mergeSearchResults 的 `name|author` 一致：
 * 同一本书多源命中时合并成一行；作者缺省时只用书名（否则作者为空的源会被拆成两条）。
 */
export function searchBookKey(name: string, author: string): string {
  const n = name.trim()
  const a = author.trim()
  return a ? `${n}|${a}` : n
}

/** 两条命中书源是否指向同一个源的同一个地址（跨页重复返回时用于去重）。 */
function sameOrigin(a: ReaderSearchOrigin, b: ReaderSearchOrigin): boolean {
  return a.source_id === b.source_id && a.book_url === b.book_url
}

/** 补齐缺失字段（对应后端 fillMissingSearchBookFields）：先到的空值不能挡掉后面源的有效值。 */
function fillMissing(dst: ReaderSearchBook, src: ReaderSearchBook): void {
  if (!dst.cover_url) dst.cover_url = src.cover_url
  if (!dst.intro) dst.intro = src.intro
  if (!dst.kind) dst.kind = src.kind
  if (!dst.word_count) dst.word_count = src.word_count
  if (!dst.latest_chapter) dst.latest_chapter = src.latest_chapter
}

/**
 * 把新一页的搜索结果并入已加载的列表（滚动加载用）。
 *
 * 每一页都是后端独立聚合出来的，跨页去重只能在前端做：书源 searchUrl 里没有
 * {{page}} 时它会重复返回首页结果，同一个源的同一本书就可能在每一页都出现，
 * 直接 append 会让列表堆满重复行。这里按 searchBookKey 合并成一项：
 * 并书源（决定「N 源可换」）、补缺失字段；顺序保持「先到的在前」，
 * 于是第 2 页的新书接在第 1 页后面。
 *
 * 返回新数组与新对象，调用方可以直接交给 setState，不必担心原地改动上一份状态。
 */
export function mergeSearchBooks(prev: ReaderSearchBook[], incoming: ReaderSearchBook[]): ReaderSearchBook[] {
  const out = prev.map((b) => ({ ...b, origins: [...b.origins] }))
  const byKey = new Map<string, ReaderSearchBook>()
  for (const b of out) byKey.set(searchBookKey(b.name, b.author), b)

  for (const b of incoming) {
    // 无名条目没有可用的聚合键（后端也会跳过 Name 为空的项），直接丢弃。
    if (!b.name.trim()) continue
    const key = searchBookKey(b.name, b.author)
    const existing = byKey.get(key)
    if (!existing) {
      const copy: ReaderSearchBook = { ...b, origins: [...b.origins] }
      out.push(copy)
      byKey.set(key, copy)
      continue
    }
    fillMissing(existing, b)
    for (const origin of b.origins) {
      if (!existing.origins.some((o) => sameOrigin(o, origin))) existing.origins.push(origin)
    }
  }
  return out
}

/**
 * 合并搜索失败的书源列表（滚动加载时每页失败的源可能不同）。
 * 按 source_id 去重，原因是「任何一页失败过就算失败」——排查页面关心的是哪些源
 * 有问题，而不是它这一页恰好成功；同一个源在多页里原因不同时取最新一页的。
 */
export function mergeSearchSkipped(
  prev: ReaderSearchSkipped[],
  incoming: ReaderSearchSkipped[],
): ReaderSearchSkipped[] {
  if (incoming.length === 0) return prev
  const out = [...prev]
  const index = new Map(out.map((s, i) => [s.source_id, i]))
  for (const s of incoming) {
    const at = index.get(s.source_id)
    if (at === undefined) {
      index.set(s.source_id, out.length)
      out.push(s)
    } else {
      out[at] = s
    }
  }
  return out
}
