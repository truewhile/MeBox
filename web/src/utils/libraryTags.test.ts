import {
  ALL_TAG_ID,
  assignLibrariesToTag,
  attachLibrariesToTag,
  attachLibraryToTag,
  buildLibraryTagTabs,
  dedupeLibraryTags,
  detachLibrariesFromTag,
  detachLibraryFromTag,
  filterLibrariesByTag,
  filterLibrariesForTagging,
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

const batchAttached = attachLibrariesToTag(
  [
    { name: '动画', library_ids: ['a', 'x'] },
    { name: '电影', library_ids: ['b'] },
  ],
  ['b', 'x', 'b', ' missing '],
  '动画',
)
check('batch attach moves every id out of its old tag', batchAttached[1].library_ids.length === 0)
check('batch attach keeps order and dedupes ids', batchAttached[0].library_ids.join(',') === 'a,b,x,missing')
check('batch attach ignores unknown target tag', attachLibrariesToTag([{ name: '动画', library_ids: [] }], ['a'], '不存在')[0].library_ids.length === 0)
check('batch attach with no ids is a no-op', attachLibrariesToTag([{ name: '动画', library_ids: ['a'] }], [], '动画')[0].library_ids.join(',') === 'a')

const batchDetached = detachLibrariesFromTag(
  [
    { name: '动画', library_ids: ['a', 'b'] },
    { name: '电影', library_ids: ['b', 'c'] },
  ],
  ['b', 'c'],
)
check('batch detach removes ids from every tag', batchDetached[0].library_ids.join(',') === 'a')
check('batch detach clears emptied tags', batchDetached[1].library_ids.length === 0)
check(
  'assign with empty tag name clears everything',
  assignLibrariesToTag([{ name: '动画', library_ids: ['a'] }], ['a'], '')[0].library_ids.length === 0,
)
check(
  'assign with a tag routes to attach',
  assignLibrariesToTag([{ name: '动画', library_ids: [] }], ['a'], '动画')[0].library_ids.join(',') === 'a',
)

const searchable = [
  { id: 'lib-a', name: '电影库', path: '/media/movies', type: 'movie' },
  { id: 'lib-b', name: 'TV Shows', path: '/media/tv', type: 'tv' },
]
check('tagging search matches name', filterLibrariesForTagging(searchable as never, '电影').length === 1)
check('tagging search matches path', filterLibrariesForTagging(searchable as never, 'media/tv').length === 1)
check('tagging search matches type case-insensitively', filterLibrariesForTagging(searchable as never, 'MOVIE').length === 1)
check('tagging search with blank keyword returns all', filterLibrariesForTagging(searchable as never, '  ').length === 2)

check('blank name is rejected', tagNameError('   ', []) !== '')
check('duplicate name is rejected', tagNameError('动画', [{ name: '动画', library_ids: [] }]) !== '')
check(
  'renaming a tag to itself is allowed',
  tagNameError('动画', [{ name: '动画', library_ids: [] }], '动画') === '',
)

console.log('libraryTags.test.ts ok')
