import {
  formatPlaybackRate,
  normalizePlaybackRate,
  stepPlaybackRate,
} from './playbackRate.ts'

function check(name: string, condition: boolean) {
  if (!condition) throw new Error(`playbackRate: ${name}`)
}

check('invalid value falls back to 1x', normalizePlaybackRate(undefined) === 1)
check('value snaps to nearest selectable rate', normalizePlaybackRate(1.2) === 1.25)
check('value below range clamps to the minimum', normalizePlaybackRate(0.1) === 0.5)
check('value above range clamps to the maximum', normalizePlaybackRate(9) === 3)
check('up steps to the next rate', stepPlaybackRate(1, 1) === 1.25)
check('down steps to the previous rate', stepPlaybackRate(1, -1) === 0.75)
check('up stops at the maximum', stepPlaybackRate(3, 1) === 3)
check('down stops at the minimum', stepPlaybackRate(0.5, -1) === 0.5)
check('integer formatting', formatPlaybackRate(1) === '1x')
check('decimal formatting', formatPlaybackRate(1.25) === '1.25x')

console.log('playbackRate.test.ts ok')
