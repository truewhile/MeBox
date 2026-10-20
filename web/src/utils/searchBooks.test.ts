import type { ReaderSearchBook, ReaderSearchOrigin } from '../api/reader.ts'
import { mergeSearchBooks, mergeSearchSkipped, searchBookKey } from './searchBooks.ts'

function check(name: string, condition: boolean) {
  if (!condition) throw new Error(`searchBooks: ${name}`)
}

function origin(sourceId: string, bookUrl: string, originName = sourceId): ReaderSearchOrigin {
  return { source_id: sourceId, origin: `https://${sourceId}.example.com`, origin_name: originName, origin_type: 0, book_url: bookUrl, latest_chapter: '' }
}

function book(over: Partial<ReaderSearchBook> & { name: string; author: string }): ReaderSearchBook {
  return {
    kind: '',
    word_count: '',
    latest_chapter: '',
    intro: '',
    cover_url: '',
    book_url: '',
    origins: [],
    ...over,
  }
}

// 聚合键：作者缺省时只用书名（和后端 name|author 的拼法一致）
check('key uses name and author', searchBookKey('全球高武', '懒人') === '全球高武|懒人')
check('key falls back to name without author', searchBookKey('全球高武', '') === '全球高武')
check('key trims blanks', searchBookKey(' 全球高武 ', ' 懒人 ') === '全球高武|懒人')
check('key treats blank author as missing', searchBookKey('全球高武', '   ') === '全球高武')

// 跨页去重：不支持分页的源在每一页都返回首页结果，不能重复追加一条书
{
  const page1 = [book({ name: '全球高武', author: '懒人', origins: [origin('a', '/1')], cover_url: 'http://c/1.jpg' })]
  const page2 = [book({ name: '全球高武', author: '懒人', origins: [origin('a', '/1')] })]
  const merged = mergeSearchBooks(page1, page2)
  check('duplicate across pages is merged', merged.length === 1)
  check('duplicate origin is not re-added', merged[0].origins.length === 1)
  check('existing fields survive the merge', merged[0].cover_url === 'http://c/1.jpg')
}

// 新一页的书接在已有书后面，顺序不倒
{
  const page1 = [book({ name: '甲', author: '作者', origins: [origin('a', '/a')] })]
  const page2 = [book({ name: '乙', author: '作者', origins: [origin('a', '/b')] })]
  const merged = mergeSearchBooks(page1, page2)
  check('new page appends', merged.length === 2)
  check('first page keeps its position', merged[0].name === '甲')
  check('second page follows', merged[1].name === '乙')
}

// 同书多源：书源并进同一项（决定「N 源可换」），且不会补齐覆盖已有的有效值
{
  const page1 = [book({ name: '全球高武', author: '懒人', origins: [origin('a', '/1')], cover_url: '', intro: '第一页的简介' })]
  const page2 = [
    book({
      name: '全球高武',
      author: '懒人',
      origins: [origin('b', '/2')],
      cover_url: 'http://c/2.jpg',
      intro: '第二页的简介',
      kind: '玄幻',
    }),
  ]
  const merged = mergeSearchBooks(page1, page2)
  check('same book merges origins', merged[0].origins.length === 2)
  check('missing cover is filled', merged[0].cover_url === 'http://c/2.jpg')
  check('missing kind is filled', merged[0].kind === '玄幻')
  check('existing intro is not overwritten', merged[0].intro === '第一页的简介')
}

// 不同作者的同名书不能被合并成一条
{
  const merged = mergeSearchBooks(
    [book({ name: '同名书', author: '甲', origins: [origin('a', '/1')] })],
    [book({ name: '同名书', author: '乙', origins: [origin('a', '/2')] })],
  )
  check('same name different author stays separate', merged.length === 2)
}

// 无名条目没有聚合键，直接丢弃（后端也会跳过 Name 为空的项）
{
  const merged = mergeSearchBooks([], [book({ name: '   ', author: '作者' })])
  check('nameless item is dropped', merged.length === 0)
}

// 不原地改动上一份状态：prev 的数组与对象都不能被写坏
{
  const prev = [book({ name: '全球高武', author: '懒人', origins: [origin('a', '/1')] })]
  const snapshot = JSON.stringify(prev)
  mergeSearchBooks(prev, [book({ name: '全球高武', author: '懒人', origins: [origin('b', '/2')] })])
  check('prev state is not mutated', JSON.stringify(prev) === snapshot)
}

// 失败书源按 source_id 去重，原因取最新一页的
{
  const merged = mergeSearchSkipped(
    [{ source_id: 'a', origin_name: '源A', reason: '超时' }],
    [
      { source_id: 'a', origin_name: '源A', reason: '限流' },
      { source_id: 'b', origin_name: '源B', reason: '解析失败' },
    ],
  )
  check('skipped is deduped by source', merged.length === 2)
  check('skipped reason comes from the newest page', merged[0].reason === '限流')
  check('new failed source is appended', merged[1].source_id === 'b')
  check('empty incoming keeps the same array', mergeSearchSkipped(merged, []) === merged)
}

console.log('searchBooks.test.ts ok')
