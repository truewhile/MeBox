import {
  ALL_GROUP_ID,
  UNGROUPED_GROUP_ID,
  attachBookToGroup,
  buildBookGroupTabs,
  dedupeBookGroups,
  detachBooksFromGroup,
  filterBooksByGroup,
  groupIdOfBook,
  normalizeBookGroups,
  reorderBookGroups,
  resolveSelectedGroupId,
} from './readerBookGroups'

// 书架分组的纯逻辑回归测试（对齐 utils/libraryTags.test.ts 的写法）。

function check(name: string, condition: boolean) {
  if (!condition) throw new Error(`readerBookGroups: ${name}`)
}

const book = (id: string) => ({ id })

// ── 清洗 ──
check('non-array → empty', normalizeBookGroups(null).length === 0)
check('blank name dropped', normalizeBookGroups([{ name: '  ', book_ids: ['a'] }]).length === 0)
check('name trimmed and ids deduped', (() => {
  const got = normalizeBookGroups([{ name: ' 科幻 ', book_ids: ['a', '', 'a', 'b'] }])
  return got.length === 1 && got[0].name === '科幻' && got[0].book_ids.join(',') === 'a,b'
})())
check('same name (case-insensitive) merged', (() => {
  const got = normalizeBookGroups([
    { name: 'SciFi', book_ids: ['a'] },
    { name: 'scifi', book_ids: ['b'] },
  ])
  return got.length === 1 && got[0].book_ids.join(',') === 'a,b'
})())

// ── 一书一组 ──
const claimed = dedupeBookGroups([
  { name: '一', book_ids: ['a', 'b'] },
  { name: '二', book_ids: ['b', 'c'] },
  { name: '三', book_ids: ['c'] },
])
check('dedupe keeps first claim', claimed[0].book_ids.join(',') === 'a,b')
// 二 原本 [b,c]：b 被 一 抢走后仍留住 c；三 的 [c] 已被 二 认领，因此变空
check('dedupe keeps the still-unclaimed member', claimed[1].book_ids.join(',') === 'c')
check('dedupe empties the group whose books were all claimed', claimed[2].book_ids.length === 0)

// ── 过滤 ──
const books = [book('a'), book('b'), book('c'), book('d')]
const groups = [{ name: '科幻', book_ids: ['a', 'b'] }, { name: '在读', book_ids: ['c'] }]
const ids = (list: { id: string }[]) => list.map((x) => x.id).join(',')

check('ALL returns everything', ids(filterBooksByGroup(books, groups, ALL_GROUP_ID)) === 'a,b,c,d')
check('empty id behaves like ALL', ids(filterBooksByGroup(books, groups, '')) === 'a,b,c,d')
check('group filter keeps members in input order', ids(filterBooksByGroup(books, groups, '科幻')) === 'a,b')
check('unknown group → empty', filterBooksByGroup(books, groups, '不存在').length === 0)
check('ungrouped returns unclaimed books', ids(filterBooksByGroup(books, groups, UNGROUPED_GROUP_ID)) === 'd')
check('stale ids in a group are ignored', (() => {
  const withStale = [{ name: '科幻', book_ids: ['a', 'ghost'] }]
  return ids(filterBooksByGroup(books, withStale, '科幻')) === 'a'
})())

// ── 分组栏 ──
const noGroups = buildBookGroupTabs(books, [])
check('no groups → only 全部', noGroups.length === 1 && noGroups[0].id === ALL_GROUP_ID && noGroups[0].count === 4)

const tabs = buildBookGroupTabs(books, groups)
check('tabs = 全部 + groups + 未分组（未分组殿后）', tabs.map((t) => t.name).join(',') === '全部,科幻,在读,未分组')
check('全部 counts all books', tabs[0].count === 4)
check('ungrouped is the last tab', tabs[tabs.length - 1].kind === 'ungrouped')
check('未分组 counts unclaimed', tabs[3].count === 1)
check('group counts only existing books', tabs[1].count === 2 && tabs[2].count === 1)
check('stale ids do not inflate counts', (() => {
  const withStale = buildBookGroupTabs(books, [{ name: '科幻', book_ids: ['a', 'ghost', 'ghost2'] }])
  return withStale[1].count === 1 && withStale[2].count === 3
})())

// ── 排序与选中回落 ──
check(
  'reorder follows the given order',
  reorderBookGroups(groups, ['在读', '科幻']).map((g) => g.name).join(',') === '在读,科幻',
)
check(
  'reorder appends unknown names (never drops)',
  reorderBookGroups(groups, ['在读']).map((g) => g.name).join(',') === '在读,科幻',
)
check('selected group falls back to ALL when removed', resolveSelectedGroupId(groups, '已删除') === ALL_GROUP_ID)
check('selected group kept when it exists', resolveSelectedGroupId(groups, '科幻') === '科幻')
check('ungrouped selection is preserved', resolveSelectedGroupId(groups, UNGROUPED_GROUP_ID) === UNGROUPED_GROUP_ID)

// ── 归组 ──
const moved = attachBookToGroup(groups, 'c', '科幻')
check('attach moves the book out of its old group', moved[1].book_ids.length === 0)
check('attach appends to the target group', moved[0].book_ids.join(',') === 'a,b,c')
check('attach to unknown group is a no-op', attachBookToGroup(groups, 'c', '不存在') === groups)
check('detach returns the book to ungrouped', detachBooksFromGroup(groups, ['a'])[0].book_ids.join(',') === 'b')
check('groupIdOfBook finds the owner', groupIdOfBook(groups, 'c') === '在读' && groupIdOfBook(groups, 'd') === '')

console.log('readerBookGroups.test.ts ok')
