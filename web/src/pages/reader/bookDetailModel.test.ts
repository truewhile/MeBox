import type { ReaderChapter, ReaderTocChapter } from '../../api/reader.ts'
import { pickChapterIndex } from './bookDetailModel.ts'

function check(name: string, condition: boolean) {
  if (!condition) throw new Error(`bookDetailModel: ${name}`)
}

function cached(index: number, url: string): ReaderChapter {
  return { index, title: `第${index}章`, url, is_volume: false }
}

function toc(index: number, url: string, over: Partial<ReaderTocChapter> = {}): ReaderTocChapter {
  return { index, title: `第${index}章`, url, is_volume: false, update_time: '', ...over }
}

// 目录一致（正常情况）：按 url 定位到的序号与现抓序号相同
{
  const list = [cached(0, 'https://s/1'), cached(1, 'https://s/2'), cached(2, 'https://s/3')]
  check('same url keeps the same index', pickChapterIndex(list, toc(2, 'https://s/3')) === 2)
}

// 站点在开头插了新章：同一章在现抓目录里是 2，在缓存目录里是 1——必须用缓存里的 1，
// 照搬现抓的 2 会跳到下一章。
{
  const list = [cached(0, 'https://s/1'), cached(1, 'https://s/2'), cached(2, 'https://s/3')]
  check('inserted chapter maps back to the cached index', pickChapterIndex(list, toc(2, 'https://s/2')) === 1)
}

// 缓存目录里没有这一章（站点改版、或两边取的不是同一份目录）：退回现抓的序号
{
  const list = [cached(0, 'https://s/1')]
  check('unknown url falls back to the fetched index', pickChapterIndex(list, toc(7, 'https://s/7')) === 7)
}

// 缓存为空（第一次打开这本书）：退回现抓序号，阅读器随后会把这份目录存下来
{
  check('empty cache falls back to the fetched index', pickChapterIndex([], toc(5, 'https://s/5')) === 5)
}

// 章节没有 url（拿不到地址）：退回现抓序号，不做 url 匹配
{
  const list = [cached(0, ''), cached(1, '')]
  check('chapter without url falls back to the fetched index', pickChapterIndex(list, toc(1, '')) === 1)
}

console.log('bookDetailModel.test.ts ok')
