// VR360（全景）播放的配置模型、命名启发式识别与本地偏好。
//
// 网页端只能从文件名/路径和画幅比例推断「这段视频是不是全景/VR」，任何信号
// 都不绝对可靠，所以这里只负责给出「大概率正确」的初始配置：识别结果不写入
// 数据库，用户随时可以在播放器菜单里改投影方式与立体布局，改动只影响本机。

export type Vr360Projection = 'equirect360' | 'equirect180' | 'fisheye180'
export type Vr360Stereo = 'mono' | 'sbs' | 'ou'

export type Vr360Profile = {
  /** 画面投影方式：360 等距柱状、180 等距柱状、180 鱼眼。 */
  projection: Vr360Projection
  /** 立体布局：单眼、左右并排、上下并排。 */
  stereo: Vr360Stereo
}

export const DEFAULT_VR360_PROFILE: Vr360Profile = {
  projection: 'equirect360',
  stereo: 'mono',
}

/** 视场角可调范围（度）：数值越小画面越「拉近」。 */
export const VR360_MIN_FOV = 35
export const VR360_MAX_FOV = 110
export const VR360_DEFAULT_FOV = 75

export const VR360_PROJECTION_OPTIONS: Array<[Vr360Projection, string]> = [
  ['equirect360', '360° 全景'],
  ['equirect180', '180° 半景'],
  ['fisheye180', '180° 鱼眼'],
]

export const VR360_STEREO_OPTIONS: Array<[Vr360Stereo, string]> = [
  ['mono', '单眼'],
  ['sbs', '左右并排'],
  ['ou', '上下并排'],
]

export function vr360ProjectionLabel(projection: Vr360Projection): string {
  return VR360_PROJECTION_OPTIONS.find(([value]) => value === projection)?.[1] ?? '360° 全景'
}

export function vr360StereoLabel(stereo: Vr360Stereo): string {
  return VR360_STEREO_OPTIONS.find(([value]) => value === stereo)?.[1] ?? '单眼'
}

export function normalizeVr360Projection(value: unknown): Vr360Projection {
  return value === 'equirect180' || value === 'fisheye180' ? value : 'equirect360'
}

export function normalizeVr360Stereo(value: unknown): Vr360Stereo {
  return value === 'sbs' || value === 'ou' ? value : 'mono'
}

export function normalizeVr360Profile(value: unknown): Vr360Profile {
  const profile = (value ?? {}) as Partial<Vr360Profile>
  return {
    projection: normalizeVr360Projection(profile.projection),
    stereo: normalizeVr360Stereo(profile.stereo),
  }
}

export function sameVr360Profile(a: Vr360Profile, b: Vr360Profile): boolean {
  return a.projection === b.projection && a.stereo === b.stereo
}

// 立体素材（左右/上下并排）在普通屏幕上只取左眼画面：真正的双眼渲染需要
// WebXR 头显，网页里把两半都画出来只会得到重复画面。
export function vr360FrameUv(stereo: Vr360Stereo): {
  scale: [number, number]
  offset: [number, number]
} {
  if (stereo === 'sbs') return { scale: [0.5, 1], offset: [0, 0] }
  if (stereo === 'ou') return { scale: [1, 0.5], offset: [0, 0] }
  return { scale: [1, 1], offset: [0, 0] }
}

export type Vr360DetectInput = {
  title?: string
  originalName?: string
  path?: string
  relativePath?: string
  /** 视频画面宽度（媒体元数据或 ffprobe 结果）。 */
  width?: number
  /** 视频画面高度。 */
  height?: number
}

export type Vr360Detection = {
  profile: Vr360Profile
  /** 文件名/路径里出现了明确的 VR 或全景关键词，可以放心自动进入 VR 模式。 */
  confident: boolean
}

// 关键词分两类：明确的关键词按子串匹配（"vr360" 里也含 "360"），
// 容易误伤的两字母缩写按独立词元匹配（避免 "1ou" / "sivr" 之类误判）。
const PANORAMA_SUBSTRINGS = ['360', 'equirect', 'insta360', 'pano', 'vuze', 'theta', '全景']
const FISHEYE_SUBSTRINGS = ['fisheye', '鱼眼']
const VR180_SUBSTRINGS = ['vr180', '180vr', '180°', '180度', '半景']
const SBS_SUBSTRINGS = ['side-by-side', 'sidebyside', 'side_by_side', '左右']
const OU_SUBSTRINGS = ['over-under', 'top-bottom', 'topbottom', '上下']
const VR_TOKENS = ['vr']
const SBS_TOKENS = ['sbs', 'hsbs', 'lr']
const OU_TOKENS = ['ou', 'tb', 'vou', 'tou']

function detectText(input: Vr360DetectInput): string {
  return [input.path, input.relativePath, input.originalName, input.title]
    .filter((value): value is string => Boolean(value))
    .join(' ')
    .toLowerCase()
}

// 文件名比标题可靠：标题里可能出现「VR」之类的普通单词，文件名里的 "vr" 通常
// 是作品编号的一部分（如 sivr-270、ipvr00192）。
function detectNameText(input: Vr360DetectInput): string {
  return [input.path, input.relativePath, input.originalName]
    .filter((value): value is string => Boolean(value))
    .join(' ')
    .toLowerCase()
}

function detectTokens(text: string): Set<string> {
  return new Set(text.split(/[^a-z0-9]+/).filter(Boolean))
}

function hasSubstring(text: string, needles: string[]): boolean {
  return needles.some((needle) => text.includes(needle))
}

function hasToken(tokens: Set<string>, needles: string[]): boolean {
  return needles.some((needle) => tokens.has(needle))
}

function aspectRatio(input: Vr360DetectInput): number | null {
  const width = input.width ?? 0
  const height = input.height ?? 0
  if (width <= 0 || height <= 0) return null
  const ratio = width / height
  return Number.isFinite(ratio) && ratio > 0 ? ratio : null
}

/**
 * 依据文件名/路径关键词与画幅比例推断 VR 配置。
 *
 * 关键词比画幅可靠：2:1 既可能是 360 单眼，也可能是 180 左右并排，所以比例只
 * 在没有明确关键词时补默认值（4:1 ⇒ 左右并排；约 1:1 的 360 ⇒ 上下并排）。
 */
export function detectVr360Profile(input: Vr360DetectInput): Vr360Detection {
  const text = detectText(input)
  const nameText = detectNameText(input)
  const tokens = detectTokens(text)

  const fisheye = hasSubstring(text, FISHEYE_SUBSTRINGS)
  const vr180 = hasSubstring(text, VR180_SUBSTRINGS)
  const panorama = hasSubstring(text, PANORAMA_SUBSTRINGS)
  const vrToken = hasToken(tokens, VR_TOKENS) || nameText.includes('vr')
  const confident = fisheye || vr180 || panorama || vrToken

  let projection: Vr360Projection = DEFAULT_VR360_PROFILE.projection
  if (fisheye) projection = 'fisheye180'
  else if (vr180) projection = 'equirect180'
  // 只有 "VR" 一个信号、没有 360/全景关键词时，按更常见的 VR180 处理。
  else if (vrToken && !panorama) projection = 'equirect180'

  const sbs = hasSubstring(text, SBS_SUBSTRINGS) || hasToken(tokens, SBS_TOKENS)
  const ou = hasSubstring(text, OU_SUBSTRINGS) || hasToken(tokens, OU_TOKENS)

  let stereo: Vr360Stereo = 'mono'
  if (sbs !== ou) stereo = sbs ? 'sbs' : 'ou'

  const ratio = aspectRatio(input)
  if (ratio !== null && stereo === 'mono') {
    if (projection === 'equirect360') {
      // 4:1 基本只可能是 360 左右并排（如 7680x1920）；
      // 约 1:1 的 360 素材多为上下并排（如 3840x3840）。
      if (ratio >= 3.4) stereo = 'sbs'
      else if (ratio <= 1.05) stereo = 'ou'
    } else {
      // VR180 单眼是 1:1，左右并排是 2:1，上下并排是 1:2。
      if (ratio >= 1.8 && ratio <= 2.2) stereo = 'sbs'
      else if (ratio <= 0.6) stereo = 'ou'
    }
  }

  return { profile: { projection, stereo }, confident }
}

const VR360_PREF_KEY = 'mebox.player.vr360'

export type Vr360Preference = {
  /** 是否按文件名/画幅自动进入 VR 模式；用户可以关掉，避免误判干扰正常播放。 */
  autoDetect: boolean
  /** 用户上次选定的投影与布局，作为下次手动进入 VR 模式时的默认值。 */
  profile: Vr360Profile
}

export const DEFAULT_VR360_PREFERENCE: Vr360Preference = {
  autoDetect: true,
  profile: DEFAULT_VR360_PROFILE,
}

export function loadVr360Preference(): Vr360Preference {
  if (typeof window === 'undefined') return DEFAULT_VR360_PREFERENCE
  try {
    const raw = window.localStorage.getItem(VR360_PREF_KEY)
    if (!raw) return DEFAULT_VR360_PREFERENCE
    const parsed = JSON.parse(raw) as Partial<Vr360Preference>
    return {
      autoDetect: parsed.autoDetect !== false,
      profile: normalizeVr360Profile(parsed.profile),
    }
  } catch {
    return DEFAULT_VR360_PREFERENCE
  }
}

export function saveVr360Preference(preference: Vr360Preference): void {
  try {
    window.localStorage.setItem(
      VR360_PREF_KEY,
      JSON.stringify({
        autoDetect: preference.autoDetect !== false,
        profile: normalizeVr360Profile(preference.profile),
      }),
    )
  } catch {
    // 隐私模式下 localStorage 不可用；播放器仍可正常使用 VR 模式。
  }
}
