import {
  DEFAULT_LIBRARY_LIST_SORT_FIELD,
  DEFAULT_LIBRARY_LIST_SORT_ORDER,
  readLibraryListSort,
  sortLibrariesByField,
  sortLibraryPreviewsByField,
} from './libraryListSort.ts'
import type { Library } from '../types/index.ts'

function check(name: string, condition: boolean) {
  if (!condition) throw new Error(`libraryListSort: ${name}`)
}

function lib(id: string, name: string, sortOrder = 0): Library {
  return { id, name, sort_order: sortOrder } as Library
}

const ordered = (libs: Library[]) => libs.map((item) => item.id).join(',')

// 首页默认就是「库名倒序」，所以没写过偏好时要拿到 name/desc。
const fallback = readLibraryListSort()
check(
  'default preference is name descending',
  fallback.field === 'name' && fallback.order === 'desc',
)
check(
  'default constants stay in sync with the fallback',
  DEFAULT_LIBRARY_LIST_SORT_FIELD === 'name' && DEFAULT_LIBRARY_LIST_SORT_ORDER === 'desc',
)

const alpha = [lib('a', '动画'), lib('b', '电影'), lib('c', '纪录片')]
// zh-CN 拼音升序是 电影(b) → 动画(a) → 纪录片(c)。
check(
  'name descending is the default order',
  ordered(sortLibrariesByField(alpha, [], 'name', 'desc')) === 'c,a,b',
)
check(
  'name ascending reverses it',
  ordered(sortLibrariesByField(alpha, [], 'name', 'asc')) === 'b,a,c',
)

const manual = [lib('a', '动画', 2), lib('b', '电影', 0), lib('c', '纪录片', 1)]
check(
  'manual order ascending follows sort_order',
  ordered(sortLibrariesByField(manual, [], 'sort_order', 'asc')) === 'b,c,a',
)
check(
  'manual order descending flips sort_order',
  ordered(sortLibrariesByField(manual, [], 'sort_order', 'desc')) === 'a,c,b',
)

// 置顶只决定分组：置顶库整体在前，组内仍然按当前字段/方向排。
check(
  'pinned libraries always come first',
  ordered(sortLibrariesByField(alpha, ['c'], 'name', 'desc')) === 'c,a,b',
)
check(
  'pinned group is sorted by the active field too',
  ordered(sortLibrariesByField(alpha, ['a', 'c'], 'name', 'desc')) === 'c,a,b',
)
check(
  'pinned ids that no longer exist are ignored',
  ordered(sortLibrariesByField(alpha, ['missing'], 'name', 'desc')) === 'c,a,b',
)

// 同名时按 sort_order 兜底，再按 id 兜底，保证顺序稳定。
const sameName = [lib('b', '动画', 1), lib('a', '动画', 1), lib('c', '动画', 0)]
check(
  'equal names fall back to sort_order then id',
  ordered(sortLibrariesByField(sameName, [], 'name', 'desc')) === 'c,a,b',
)

check('single library is returned as-is', sortLibrariesByField([lib('a', '动画')], [], 'name', 'desc').length === 1)

const previews = [
  { library: lib('a', '动画'), total: 1 },
  { library: lib('b', '电影'), total: 2 },
]
check(
  'previews follow the library order',
  sortLibraryPreviewsByField(previews, [], 'name', 'desc').map((item) => item.library.id).join(',') === 'a,b',
)
check(
  'previews keep their payload after sorting',
  sortLibraryPreviewsByField(previews, [], 'name', 'desc')[0].total === 1,
)
check(
  'previews respect pinning',
  sortLibraryPreviewsByField(previews, ['b'], 'name', 'desc').map((item) => item.library.id).join(',') === 'b,a',
)

console.log('libraryListSort.test.ts ok')
