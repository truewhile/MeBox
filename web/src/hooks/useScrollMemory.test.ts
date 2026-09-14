import { shouldRememberScroll } from './useScrollMemory.ts'

function check(name: string, condition: boolean) {
  if (!condition) throw new Error(`useScrollMemory: ${name}`)
}

check('home remembers scroll', shouldRememberScroll('/'))
check('library list remembers scroll', shouldRememberScroll('/libraries'))
check('library detail remembers scroll', shouldRememberScroll('/library/lib-1'))
check('media detail remembers scroll', shouldRememberScroll('/media/media-1'))
check('settings does not remember scroll', !shouldRememberScroll('/settings'))
check('player does not remember scroll', !shouldRememberScroll('/play/media-1'))

console.log('useScrollMemory.test.ts ok')
