// 书籍详情页点目录跳读的纯逻辑（不依赖 React，便于单测）。

import type { ReaderChapter, ReaderTocChapter } from '../../api/reader'

/**
 * 把详情页点中的章节换算成阅读器要的序号。
 *
 * 阅读器打开一本书时优先用库里缓存的目录（`/books/:id/chapters`），而详情页显示的
 * 目录是现抓的：站点更新过目录（插入或调整过章节）时两者会错位，直接照搬现抓目录
 * 里的序号会跳到别的章。url 是章节的稳定标识，所以先按 url 在缓存目录里定位；
 * 找不到（缓存为空，或两边取的根本不是同一份目录）才退回现抓的序号——缓存为空时
 * 阅读器本来就会把这次现抓的目录存下来，序号自然对得上。
 */
export function pickChapterIndex(cached: ReaderChapter[], chapter: ReaderTocChapter): number {
  if (chapter.url) {
    const hit = cached.findIndex((c) => c.url === chapter.url)
    if (hit >= 0) return hit
  }
  return chapter.index
}
