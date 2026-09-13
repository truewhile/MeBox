import { runWithoutGlobalVideoFrame } from './withoutVideoFrame.ts'

function check(name: string, cond: boolean) {
  if (!cond) throw new Error(`runWithoutGlobalVideoFrame: ${name}`)
}

const FakeVideoFrame = function VideoFrame() {
  return undefined
}

Object.defineProperty(globalThis, 'VideoFrame', {
  configurable: true,
  writable: true,
  value: FakeVideoFrame,
})

let seenInside = typeof (globalThis as { VideoFrame?: unknown }).VideoFrame
const result = runWithoutGlobalVideoFrame(() => {
  seenInside = typeof (globalThis as { VideoFrame?: unknown }).VideoFrame
  return 7
})

check('hides VideoFrame inside the callback', seenInside === 'undefined')
check('returns the callback result', result === 7)
check(
  'restores VideoFrame afterwards',
  (globalThis as { VideoFrame?: unknown }).VideoFrame === FakeVideoFrame,
)

console.log('withoutVideoFrame.test.ts ok')
