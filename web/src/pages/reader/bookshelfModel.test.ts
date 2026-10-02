import type { ReaderBook } from '../../api/reader'
import { formatRelativeTime, sortShelfBooks, unreadChapters } from './bookshelfModel'

function check(name: string, condition: boolean) {
  if (!condition) throw new Error(`bookshelfModel: ${name}`)
}

function book(overrides: Partial<ReaderBook>): ReaderBook {
  return {
    id: overrides.id ?? 'x',
    origin: '',
    origin_name: '',
    book_url: '',
    toc_url: '',
    name: '书',
    author: '',
    kind: '',
    cover_url: '',
    intro: '',
    type: 0,
    latest_chapter_title: '',
    latest_chapter_time: 0,
    total_chapter_num: 0,
    dur_chapter_index: 0,
    dur_chapter_pos: 0,
    dur_chapter_title: '',
    dur_chapter_time: 0,
    order: 0,
    is_local: false,
    local_external: false,
    open_credits: 0,
    close_credits: 0,
    ...overrides,
  } as ReaderBook
}

// ── 未读章数 ──
check('no cached toc → null', unreadChapters(book({ total_chapter_num: 0, dur_chapter_index: 3 })) === null)
check('unread book counts all chapters', unreadChapters(book({ total_chapter_num: 10 })) === 10)
check(
  'reading chapter 3 of 10 leaves 7',
  unreadChapters(book({ total_chapter_num: 10, dur_chapter_index: 2, dur_chapter_time: 1 })) === 7,
)
check(
  'progress past the end clamps to 0',
  unreadChapters(book({ total_chapter_num: 5, dur_chapter_index: 99, dur_chapter_time: 1 })) === 0,
)
// dur_chapter_time 为 0 表示还没开始读，即使 index > 0 也不该算作已读
check('zero dur_chapter_time means unread', unreadChapters(book({ total_chapter_num: 4, dur_chapter_index: 2 })) === 4)

// ── 排序 ──
const a = book({ id: 'a', name: 'β', dur_chapter_time: 100, latest_chapter_time: 500, order: 2 })
const b = book({ id: 'b', name: 'α', dur_chapter_time: 300, latest_chapter_time: 100, order: 1 })
const c = book({ id: 'c', name: 'γ', dur_chapter_time: 0, latest_chapter_time: 900, order: 3 })

check('recent sorts by read time desc', sortShelfBooks([a, b, c], 'recent').map((x) => x.id).join('') === 'bac')
check('update sorts by latest chapter time desc', sortShelfBooks([a, b, c], 'update').map((x) => x.id).join('') === 'cab')
check('manual sorts by order asc', sortShelfBooks([a, b, c], 'manual').map((x) => x.id).join('') === 'bac')
check('name sorts by title', sortShelfBooks([a, b, c], 'name').map((x) => x.id).join('') === 'bac')
check(
  'mixed uses the newer of read/update time',
  sortShelfBooks([a, b, c], 'mixed').map((x) => x.id).join('') === 'cab',
)
check('sort does not mutate the input', [a, b, c].map((x) => x.id).join('') === 'abc')

// 作者相同按书名兜底
const d1 = book({ id: 'd1', name: 'B', author: '同一作者' })
const d2 = book({ id: 'd2', name: 'A', author: '同一作者' })
check('author then title', sortShelfBooks([d1, d2], 'author').map((x) => x.id).join('') === 'd2d1')

// ── 时间格式化 ──
check('zero time → empty', formatRelativeTime(0) === '')
check('just now', formatRelativeTime(Date.now() - 5_000) === '刚刚')
check('minutes ago', formatRelativeTime(Date.now() - 5 * 60_000) === '5 分钟前')
check('hours ago', formatRelativeTime(Date.now() - 3 * 3_600_000) === '3 小时前')
check('days ago', formatRelativeTime(Date.now() - 2 * 86_400_000) === '2 天前')

console.log('bookshelfModel.test.ts ok')
