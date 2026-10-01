import {
  canSeekDirectNow,
  seekableEndSec,
  timeInSeekableRanges,
} from './directPlaySeek.ts'

function check(name: string, cond: boolean) {
  if (!cond) throw new Error(`directPlaySeek: ${name}`)
}

function ranges(parts: Array<[number, number]>) {
  return {
    length: parts.length,
    start: (i: number) => parts[i][0],
    end: (i: number) => parts[i][1],
  }
}

check('empty seekable rejects mid seek', !timeInSeekableRanges(null, 120))
check('empty seekable end is 0', seekableEndSec(undefined) === 0)

check(
  'covered range accepts target',
  timeInSeekableRanges(ranges([[0, 600]]), 120),
)

check(
  'target beyond end is rejected',
  !timeInSeekableRanges(ranges([[0, 30]]), 120),
)

check(
  'multi-range uses any segment',
  timeInSeekableRanges(
    ranges([
      [0, 10],
      [100, 200],
    ]),
    150,
  ),
)

check('seekableEndSec takes max end', seekableEndSec(ranges([[0, 10], [5, 80]])) === 80)

check('canSeekDirectNow allows near-zero without seekable', canSeekDirectNow(null, 0.2))
check('canSeekDirectNow blocks mid when not seekable', !canSeekDirectNow(null, 90))
check(
  'canSeekDirectNow allows mid when covered',
  canSeekDirectNow(ranges([[0, 900]]), 90),
)

console.log('directPlaySeek.test.ts ok')
