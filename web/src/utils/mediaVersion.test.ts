import {
  autoPlayEpisodeFor,
  isStrmMedia,
  mediaVersionFileName,
  mediaVersionLabel,
  mediaVersionMatches,
  mediaVersionSourceLabel,
} from './mediaVersion.ts'
import type { Media } from '../types/index.ts'

function check(name: string, condition: boolean) {
  if (!condition) throw new Error(`mediaVersion: ${name}`)
}

function media(partial: Partial<Media>): Media {
  return {
    id: 'm1',
    title: 'SIVR-270',
    path: '',
    height: 0,
    width: 0,
    size_bytes: 0,
    ...partial,
  } as Media
}

const partLabel = mediaVersionLabel(media({
  path: '/media/云下载/sivr-270/sivr-270-1.strm',
  size_bytes: 157,
  strm_url: '/api/strm/play/cloud115/video.mp4?pickcode=a',
}))
check('indistinct strm falls back to filename', partLabel.includes('sivr-270-1'))
check('indistinct strm is not generic MP4 only', partLabel.toUpperCase() !== 'MP4')

const richLabel = mediaVersionLabel(media({
  path: '/strm/竞女01.mkv.strm',
  height: 1080,
  size_bytes: 1024 * 1024 * 1200,
  strm_url: '/api/strm/play/cloud115/video.mkv?pickcode=a',
}))
check('rich metadata keeps resolution label', richLabel.includes('1080p'))
check('rich metadata keeps container', richLabel.toUpperCase().includes('MKV'))

const dottedPart = mediaVersionLabel(media({
  path: '/media/云下载/ipvr00192pl/fbzip.com@ipvr00192.part2.strm',
  size_bytes: 157,
  strm_url: '/api/strm/play/cloud115/video.mp4?pickcode=b',
}))
check('part2 filename fallback keeps part token', dottedPart.includes('part2'))

// 折叠后的选集行 id 是最优版本；播放中的可能是组内另一个版本。
const local4k = media({ id: 'v-4k', path: '/media/Inception.2010.2160p.mkv', height: 2160 })
const local1080 = media({ id: 'v-1080', path: '/media/Inception.2010.1080p.mkv', height: 1080 })
const cloudRow = media({ id: 'v-cloud', path: '/strm/Inception.2010.mkv.strm', strm_url: '/api/strm/play/cloud115/video.mkv?pickcode=a' })
const groupedRow = media({ id: 'v-4k', versions: [local4k, local1080, cloudRow] })

check('matches own id', mediaVersionMatches(groupedRow, 'v-4k'))
check('matches nested version id', mediaVersionMatches(groupedRow, 'v-1080'))
check('rejects unrelated id', !mediaVersionMatches(groupedRow, 'v-other'))
check('rejects empty id', !mediaVersionMatches(groupedRow, ''))

check('strm is cloud source', mediaVersionSourceLabel(cloudRow) === '云端直链')
check('mkv is local source', mediaVersionSourceLabel(local1080) === '本地文件')
check('strm filename drops outer extension', mediaVersionFileName(cloudRow) === 'Inception.2010.mkv')
check('missing path has no filename', mediaVersionFileName(media({ path: '' })) === '')
check('isStrmMedia detects strm url', isStrmMedia(cloudRow))

// 首页轮播/剧集卡片交给播放器的是「整部剧」ID：选集列表里没有它自己，
// 播放器要自动落到第一集，否则 /api/stream/<剧集ID> 会被远程 Emby 打回 500。
const series = media({ id: 'embyremote~mount-1~222679', title: '某部新番', path: '/anime/某部新番', size_bytes: 0 })
const ep1 = media({ id: 'embyremote~mount-1~222681', path: '/anime/某部新番/Season 1/S1E01.mp4', size_bytes: 299580311 })
const ep2 = media({ id: 'embyremote~mount-1~222683', path: '/anime/某部新番/Season 1/S1E02.mp4', size_bytes: 299580312 })

check('series falls back to its first episode', autoPlayEpisodeFor(series, series.id, [ep1, ep2])?.id === ep1.id)
check('playable episode stays untouched', autoPlayEpisodeFor(ep1, ep1.id, [ep1, ep2]) === null)
check('single-item movie list stays untouched', autoPlayEpisodeFor(ep1, ep1.id, [{ ...ep1 }]) === null)
check('waits until the requested item is loaded', autoPlayEpisodeFor(null, series.id, [ep1]) === null)
check('ignores a response for another id', autoPlayEpisodeFor(ep2, series.id, [ep1]) === null)
check('empty episode list has no fallback', autoPlayEpisodeFor(series, series.id, []) === null)
check(
  'entry present as another version stays untouched',
  autoPlayEpisodeFor(
    media({ id: 'v-4k' }),
    'v-4k',
    [media({ id: 'v-4k', versions: [local4k, local1080] })],
  ) === null,
)

console.log('mediaVersion.test.ts: ok')
