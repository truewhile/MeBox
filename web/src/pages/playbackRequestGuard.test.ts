import { strict as assert } from 'node:assert'
import { createPlaybackRequestGuard } from './playbackRequestGuard.ts'

const guard = createPlaybackRequestGuard()
const first = guard.begin()
assert.equal(first(), true)
const second = guard.begin()
assert.equal(first(), false, 'a newer quality request supersedes the earlier request')
assert.equal(second(), true)
guard.invalidate()
assert.equal(second(), false, 'media changes, unmount and direct/local switches cancel a pending cloud result')
let resolveRequest: (value: string) => void = () => {}
const request = new Promise<string>(resolve => { resolveRequest = resolve })
const current = guard.begin()
let applied = 'episode-B'
const settled = request.then(value => { if (current()) applied = value })
guard.invalidate()
resolveRequest('episode-A')
await settled
assert.equal(applied, 'episode-B', 'a delayed response cannot overwrite the next media')
let rejected: (error: Error) => void = () => {}
const failed = new Promise<never>((_, reject) => { rejected = reject })
const activeFailure = guard.begin()
let fallbackCount = 0
const caught = failed.catch(() => { if (activeFailure()) fallbackCount += 1 })
guard.invalidate()
rejected(new Error('old request failed'))
await caught
assert.equal(fallbackCount, 0, 'a stale rejection cannot force a local fallback')
console.log('playbackRequestGuard.test.ts ok')
