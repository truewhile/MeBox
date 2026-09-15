import { shouldPersistScrollSample, shouldRememberScroll } from './useScrollMemory.ts'

function check(name: string, condition: boolean) {
  if (!condition) throw new Error(`useScrollMemory: ${name}`)
}

check('home remembers scroll', shouldRememberScroll('/'))
check('library list remembers scroll', shouldRememberScroll('/libraries'))
check('library detail remembers scroll', shouldRememberScroll('/library/lib-1'))
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

console.log('useScrollMemory.test.ts ok')
