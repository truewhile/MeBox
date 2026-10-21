import { strict as assert } from 'node:assert'
import { availableLocalQuality, findPlaybackQualityById } from './playbackQuality.ts'
import type { PlaybackInfo } from '../types/playback.ts'

const info: PlaybackInfo = {
  media_id: 'm', provider: 'cloud115', default_quality: '4', fallback: [],
  transcode: { state: 'idle' },
  cloud_qualities: [{ id: '4', label: '1080P', source: 'cloud', available: false }],
  local_qualities: [
    { id: 'source', label: '原画', source: 'local', available: false },
    { id: '1080', label: '1080P', source: 'local', available: true },
    { id: '720', label: '720P', source: 'local', available: true },
  ],
}
assert.equal(availableLocalQuality(info, '4')?.id, '1080', 'pending cloud quality must not mask the available local fallback')
assert.equal(availableLocalQuality(info, '720')?.id, '720', 'preserve an available local preference')
assert.equal(availableLocalQuality({ ...info, local_qualities: info.local_qualities.map(q => ({ ...q, available: false })) }, '4'), undefined)
assert.equal(findPlaybackQualityById(info, '4')?.source, 'cloud')
console.log('playbackQuality.test.ts ok')
