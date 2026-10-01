export type SubtitleChineseMode = 'original' | 'simplified' | 'traditional'

type ChineseConverter = (text: string) => string

const identity: ChineseConverter = (text) => text

export function normalizeSubtitleChineseMode(value: unknown): SubtitleChineseMode {
  return value === 'simplified' || value === 'traditional' ? value : 'original'
}

let simplifiedConverter: Promise<ChineseConverter> | undefined
let traditionalConverter: Promise<ChineseConverter> | undefined

export function loadSubtitleChineseConverter(
  mode: SubtitleChineseMode,
): Promise<ChineseConverter> {
  if (mode === 'original') return Promise.resolve(identity)

  if (mode === 'simplified') {
    simplifiedConverter ??= import('opencc-js/t2cn').then(({ Converter }) =>
      Converter({ from: 't', to: 'cn' }),
    )
    return simplifiedConverter
  }

  traditionalConverter ??= import('opencc-js/cn2t').then(({ Converter }) =>
    Converter({ from: 'cn', to: 'tw' }),
  )
  return traditionalConverter
}
