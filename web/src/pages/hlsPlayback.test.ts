import { strict as assert } from 'node:assert'
import { applyPendingHlsSeek, attachNativeHls, bindPendingHlsSeek, hlsManifestTimeoutMs, playbackPositionSec } from './hlsPlayback.ts'

class FakeVideo extends EventTarget {
  src = ''
  rejectSeek = false
  position = 0
  playCalls = 0
  get currentTime() { return this.position }
  set currentTime(value: number) {
    if (this.rejectSeek) throw new Error('metadata not ready')
    this.position = value
  }
  play() { this.playCalls += 1; return Promise.resolve() }
}

const video = new FakeVideo()
const pending = { current: 420 as number | null }
const detach = attachNativeHls(video, '/cloud/master.m3u8', pending)
assert.equal(video.src, '/cloud/master.m3u8')
assert.equal(video.playCalls, 1)
assert.equal(video.currentTime, 0, 'seek must wait for the new source metadata')
video.rejectSeek = true
video.dispatchEvent(new Event('loadedmetadata'))
assert.equal(pending.current, 420, 'a rejected seek must survive until canplay')
video.rejectSeek = false
video.dispatchEvent(new Event('canplay'))
assert.equal(video.currentTime, 420, 'native HLS must restore the absolute cloud position')
assert.equal(pending.current, null)
pending.current = 600
detach()
video.dispatchEvent(new Event('loadedmetadata'))
assert.equal(video.currentTime, 420, 'a detached source must not apply late metadata events')
assert.equal(pending.current, 600)

const msePending = { current: 120 as number | null }
video.rejectSeek = true
assert.equal(applyPendingHlsSeek(video, msePending), false)
assert.equal(msePending.current, 120)
video.rejectSeek = false
assert.equal(applyPendingHlsSeek(video, msePending), true)
assert.equal(msePending.current, null)
const lateMsePending = { current: 900 as number | null }
video.rejectSeek = true
const detachMse = bindPendingHlsSeek(video, lateMsePending)
assert.equal(applyPendingHlsSeek(video, lateMsePending), false)
video.rejectSeek = false
video.dispatchEvent(new Event('loadedmetadata'))
assert.equal(video.currentTime, 900, 'hls.js seeks rejected during manifest parsing must retry after metadata')
assert.equal(lateMsePending.current, null)
detachMse()
assert.ok(hlsManifestTimeoutMs('local', 1800) > 120_000, 'deep seeks must outlast the server readiness deadline')
assert.equal(hlsManifestTimeoutMs('local', 0), 60_000)
assert.equal(hlsManifestTimeoutMs('cloud', 1800), 60_000)
assert.equal(playbackPositionSec('hls', 'cloud', 420, 600), 600, 'cloud subtitle/quality switches must not add an old local offset')
assert.equal(playbackPositionSec('hls', 'local', 420, 60), 480, 'local subtitle switches preserve the source position')
assert.equal(playbackPositionSec('direct', 'local', 420, 60), 60)
console.log('hlsPlayback.test.ts ok')
