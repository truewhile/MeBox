import {
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

console.log('mediaVersion.test.ts: ok')
