import { classifyDirectPlayError } from './directPlayError.ts'

function check(name: string, cond: boolean) {
  if (!cond) throw new Error(`classifyDirectPlayError: ${name}`)
}

const src = 'http://nas.local/api/stream/m1?token=abc'

check(
  'aborted load is ignored',
  classifyDirectPlayError({
    errorCode: 1,
    readyState: 0,
    elementSrc: src,
    expectedSrc: src,
    alreadyRetried: false,
  }) === 'ignore',
)

check(
  'already-playing error is ignored',
  classifyDirectPlayError({
    errorCode: 4,
    readyState: 3,
    elementSrc: src,
    expectedSrc: src,
    alreadyRetried: false,
  }) === 'ignore',
)

check(
  'stale src error is ignored',
  classifyDirectPlayError({
    errorCode: 4,
    readyState: 0,
    elementSrc: 'http://nas.local/old',
    expectedSrc: src,
    alreadyRetried: false,
  }) === 'ignore',
)

check(
  'empty src during reload is ignored',
  classifyDirectPlayError({
    errorCode: 4,
    readyState: 0,
    elementSrc: '',
    expectedSrc: src,
    alreadyRetried: false,
  }) === 'ignore',
)

check(
  'first decode/network miss retries',
  classifyDirectPlayError({
    errorCode: 4,
    readyState: 0,
    elementSrc: src,
    expectedSrc: src,
    alreadyRetried: false,
  }) === 'retry',
)

check(
  'second failure falls back to HLS',
  classifyDirectPlayError({
    errorCode: 4,
    readyState: 0,
    elementSrc: src,
    expectedSrc: src,
    alreadyRetried: true,
  }) === 'fallback',
)

console.log('playerPageModel.test.ts ok')
