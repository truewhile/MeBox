import {
  ALL_TAG_ID,
  attachLibraryToTag,
  buildLibraryTagTabs,
  dedupeLibraryTags,
  detachLibraryFromTag,
  filterLibrariesByTag,
  normalizeLibraryTags,
  normalizeTagName,
  resolveSelectedTagId,
  tagNameError,
} from './libraryTags.ts'

function check(name: string, condition: boolean) {
  if (!condition) throw new Error(`libraryTags: ${name}`)
}

const libs = [
  { id: 'lib-a', total: 10 },
  { id: 'lib-b', total: 5 },
  { id: 'lib-c', total: 0 },
]

check('empty input normalizes to empty list', normalizeLibraryTags(null).length === 0)
check('trims and drops blank names', normalizeLibraryTags([{ name: '  ', library_ids: [] }]).length === 0)
check(
  'merges duplicate tag names case-insensitively',
  normalizeLibraryTags([
    { name: 'Anime', library_ids: ['a'] },
    { name: 'anime', library_ids: ['b', 'a'] },
  ]).length === 1,
)
check(
  'dedupes ids inside one tag',
  normalizeLibraryTags([{ name: '动画', library_ids: ['a', '', 'a', 'b'] }])[0].library_ids.join(',') === 'a,b',
)
check('truncates over-long names to 24 chars', Array.from(normalizeTagName('x'.repeat(40))).length === 24)

const tabs = buildLibraryTagTabs(libs, [
  { name: '动画', library_ids: ['lib-b', 'missing'] },
  { name: '电影', library_ids: [] },
])
check('tab bar always starts with 全部', tabs[0].id === ALL_TAG_ID && tabs[0].count === 3)
check('tag tab keeps creation order', tabs[1].name === '动画' && tabs[2].name === '电影')
check('tag count ignores unavailable libraries', tabs[1].count === 1)

check(
  'filtering keeps tag order and drops unknown ids',
  filterLibrariesByTag(libs, [{ name: '动画', library_ids: ['lib-c', 'missing', 'lib-a'] }], '动画')
    .map((lib) => lib.id)
    .join(',') === 'lib-c,lib-a',
)
check('全部 returns every library in original order', filterLibrariesByTag(libs, [], ALL_TAG_ID).length === 3)
check('unknown tag yields no libraries', filterLibrariesByTag(libs, [], 'nope').length === 0)

check(
  'selected tag falls back to 全部 when the tag is gone',
  resolveSelectedTagId([{ name: '动画', library_ids: [] }], '动画') === '动画' &&
    resolveSelectedTagId([{ name: '动画', library_ids: [] }], '电影') === ALL_TAG_ID,
)

const claimed = dedupeLibraryTags([
  { name: '动画', library_ids: ['a', 'b'] },
  { name: '电影', library_ids: ['b', 'c'] },
])
check('one library belongs to a single tag', claimed[0].library_ids.join(',') === 'a,b')
check('later tag loses the shared library', claimed[1].library_ids.join(',') === 'c')

const attached = attachLibraryToTag(
  [
    { name: '动画', library_ids: ['a'] },
    { name: '电影', library_ids: ['b'] },
  ],
  'a',
  '电影',
)
check('attach moves the library out of its old tag', attached[0].library_ids.length === 0)
check('attach appends to the target tag', attached[1].library_ids.join(',') === 'b,a')

const detached = detachLibraryFromTag(
  [{ name: '电影', library_ids: ['b', 'a'] }],
  'a',
  '电影',
)
check('detach removes only the requested library', detached[0].library_ids.join(',') === 'b')

check('blank name is rejected', tagNameError('   ', []) !== '')
check('duplicate name is rejected', tagNameError('动画', [{ name: '动画', library_ids: [] }]) !== '')
check(
  'renaming a tag to itself is allowed',
  tagNameError('动画', [{ name: '动画', library_ids: [] }], '动画') === '',
)

console.log('libraryTags.test.ts ok')
