import {
  isJumpIntentKey,
  isScrollIntentKey,
  shouldReapplyGuardedScroll,
  shouldPersistClampedSample,
  shouldPersistScrollSample,
  shouldRememberScroll,
} from './useScrollMemory.ts'

function check(name: string, condition: boolean) {
  if (!condition) throw new Error(`useScrollMemory: ${name}`)
}

check('home remembers scroll', shouldRememberScroll('/'))
check('media search remembers scroll', shouldRememberScroll('/search'))
check('library list remembers scroll', shouldRememberScroll('/libraries'))
check('library detail remembers scroll', shouldRememberScroll('/library/lib-1'))
check('library series panel remembers scroll', shouldRememberScroll('/library/lib-1'))
check('media detail remembers scroll', shouldRememberScroll('/media/media-1'))
check('settings does not remember scroll', !shouldRememberScroll('/settings'))
check('player does not remember scroll', !shouldRememberScroll('/play/media-1'))

check(
  'route transition collapse is ignored',
  !shouldPersistScrollSample({
    current: 0,
    lastSaved: 1800,
    height: 400,
    lastHeight: 4200,
  }),
)
check(
  'intentional scroll to top is persisted',
  shouldPersistScrollSample({
    current: 0,
    lastSaved: 1800,
    height: 4200,
    lastHeight: 4200,
  }),
)
check(
  'normal downward scroll is persisted',
  shouldPersistScrollSample({
    current: 1900,
    lastSaved: 1800,
    height: 4200,
    lastHeight: 4200,
  }),
)
check(
  'content growth still allows persist',
  shouldPersistScrollSample({
    current: 1800,
    lastSaved: 1800,
    height: 5200,
    lastHeight: 4200,
  }),
)

// 恢复途中内容还没长出来（滚不动）或被钳在矮内容底部时，scrollTop 是浏览器钳制
// 出来的值，不能写回存储，否则一次误触就把记忆位置清成 0。
check(
  'clamped sample while list is still loading is ignored',
  !shouldPersistClampedSample({ current: 0, maxScroll: 0, saved: 1800 }),
)
check(
  'clamped sample at the bottom of a shrunken list is ignored',
  !shouldPersistClampedSample({ current: 1200, maxScroll: 1200, saved: 1800 }),
)
check(
  'sample at the bottom of a completely scrolled list stays persistable',
  shouldPersistClampedSample({ current: 1800, maxScroll: 1800, saved: 1800 }),
)
check(
  'reachable scroll position is persisted',
  shouldPersistClampedSample({ current: 900, maxScroll: 1200, saved: 1800 }),
)
check(
  'reaching the remembered target is persisted',
  shouldPersistClampedSample({ current: 1800, maxScroll: 2600, saved: 1800 }),
)
check(
  'deeper than the remembered target is persisted',
  shouldPersistClampedSample({ current: 3000, maxScroll: 4000, saved: 1800 }),
)
check(
  'intentional scroll to top of a tall list is persisted',
  shouldPersistClampedSample({ current: 0, maxScroll: 4000, saved: 1800 }),
)
check(
  'unscrollable page without memory cannot persist',
  !shouldPersistClampedSample({ current: 0, maxScroll: 0, saved: 0 }),
)

check('page down counts as scroll intent', isScrollIntentKey('PageDown'))
check('space counts as scroll intent', isScrollIntentKey(' '))
check('typing does not count as scroll intent', !isScrollIntentKey('a'))
check('shortcut does not count as scroll intent', !isScrollIntentKey('Meta'))
check('page down allows a long jump', isJumpIntentKey('PageDown'))
check('home allows a long jump', isJumpIntentKey('Home'))
check('arrow key keeps smooth-only intent', !isJumpIntentKey('ArrowDown'))
check('space keeps smooth-only intent', !isJumpIntentKey(' '))

// 恢复完成后的看守期：列表重排（普通网格换成虚拟列表）会把容器高度压塌，
// scrollTop 被浏览器夹小；没有用户滚动意图时要把记忆位置贴回去。
check(
  'collapsed position inside the guard window is re-applied',
  shouldReapplyGuardedScroll({
    current: 0,
    target: 2600,
    maxScroll: 30000,
    userDriven: false,
    now: 1000,
    guardUntil: 3000,
  }),
)
check(
  'user scroll during the guard window wins',
  !shouldReapplyGuardedScroll({
    current: 0,
    target: 2600,
    maxScroll: 30000,
    userDriven: true,
    now: 1000,
    guardUntil: 3000,
  }),
)
check(
  'no re-apply after the guard window',
  !shouldReapplyGuardedScroll({
    current: 0,
    target: 2600,
    maxScroll: 30000,
    userDriven: false,
    now: 4000,
    guardUntil: 3000,
  }),
)
check(
  'no re-apply while the content is still too short',
  !shouldReapplyGuardedScroll({
    current: 0,
    target: 0,
    maxScroll: 0,
    userDriven: false,
    now: 1000,
    guardUntil: 3000,
  }),
)
check(
  'position already at the target is left alone',
  !shouldReapplyGuardedScroll({
    current: 2600,
    target: 2600,
    maxScroll: 30000,
    userDriven: false,
    now: 1000,
    guardUntil: 3000,
  }),
)

console.log('useScrollMemory.test.ts ok')
