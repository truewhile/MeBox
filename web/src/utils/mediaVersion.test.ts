import { mediaVersionLabel } from './mediaVersion.ts'
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

console.log('mediaVersion.test.ts: ok')
