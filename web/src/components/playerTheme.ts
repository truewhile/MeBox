/**
 * 播放器浮层的统一视觉语言。
 *
 * 播放器里现在有十几种浮层：底部操作栏、清晰度/倍速/字幕小弹层、设置面板、
 * 选集抽屉、弹幕面板、VR 工具条、错误提示……以前每一处都自己写一遍圆角、
 * 边框、透明度，于是同一屏里出现了好几种不同的浮层样式，看起来就「乱」。
 *
 * 这里把冒泡、排版、图标按钮等重复的类名收敛成常量：改一次全部同步，
 * 新加控件也不会再各写一套。
 */

/** 面板底色。比纯黑更「实」，配合 backdrop-blur 后压在画面上仍然清晰可读。 */
const PANEL_BG = 'bg-[#0d0e12]/95'

/** 浮层外圈阴影：暗背景下用大范围柔光，避免出现一圈生硬的黑边。 */
const PANEL_SHADOW = 'shadow-[0_10px_38px_rgba(0,0,0,0.6)]'

/** 小弹层（清晰度/倍速/字幕/设置）：贴着操作栏向上弹出。 */
export const PLAYER_POPOVER =
  `z-40 overflow-hidden rounded-xl border border-white/10 ${PANEL_BG} py-1 text-white ${PANEL_SHADOW} backdrop-blur-xl`

/**
 * 右侧抽屉（选集/弹幕设置）：整条贴住播放区域右边缘，通到顶底。
 *
 * 小屏上留出 12% 的宽度而不是铺满：这样顶栏的返回按钮和标题仍然露在外面，
 * 用户不用先关抽屉才能返回（铺满时唯一的出口只剩标题栏上的关闭按钮）。
 */
export const PLAYER_DRAWER =
  `flex h-full w-[88%] flex-col border-white/10 ${PANEL_BG} text-white ${PANEL_SHADOW} backdrop-blur-xl sm:w-[380px] sm:border-l`

/**
 * 底部动作面板（竖屏剧场模式）：贴着视频区底部向上弹出，内容最多占
 * 视频区高度的 72%，留出一些画面可见；底部垫上安全区高度，避免
 * iPhone 小横条遮挡最后一行选项。
 */
export const PLAYER_SHEET =
  `z-40 flex max-h-[72%] min-h-[8rem] w-full flex-col overflow-hidden rounded-t-2xl border border-white/10 border-b-0 ${PANEL_BG} text-white ${PANEL_SHADOW} backdrop-blur-xl`

/** 底部动作面板的标题栏（带关闭按钮）。 */
export const PLAYER_SHEET_HEADER =
  'flex shrink-0 items-center justify-between gap-2 border-b border-white/10 px-4 py-2.5'

/** 底部动作面板的可滚动内容区，底部垫上安全区高度。 */
export const PLAYER_SHEET_BODY =
  'min-h-0 flex-1 overflow-y-auto overscroll-contain px-2 pb-[calc(0.75rem+env(safe-area-inset-bottom,0px))] pt-1'

/** 操作栏里的图标按钮。所有图标按钮共用同一尺寸与悬停反馈，排在一起才整齐。 */
export const PLAYER_ICON_BUTTON =
  'flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-white/85 transition hover:bg-white/15 hover:text-white disabled:cursor-not-allowed disabled:text-white/30 disabled:hover:bg-transparent sm:h-8 sm:w-8'

/** 带文字的按钮（清晰度/倍速/选集）。文字按钮和图标按钮同高，基线才对得齐。 */
export const PLAYER_TEXT_BUTTON =
  'flex h-7 shrink-0 items-center gap-1 rounded-md px-1.5 text-[11px] font-medium text-white/85 transition hover:bg-white/15 hover:text-white disabled:cursor-not-allowed disabled:text-white/30 disabled:hover:bg-transparent sm:h-8 sm:px-2 sm:text-xs'

/** 弹层/面板里的一行选项。 */
export const PLAYER_MENU_ITEM =
  'flex w-full items-center gap-2 rounded-lg px-2.5 py-1.5 text-left text-xs text-white/85 transition hover:bg-white/10 hover:text-white'

/** 弹层里的小标题（「115 云端」「本地 HLS」这类分组名）。 */
export const PLAYER_MENU_LABEL =
  'px-3 pb-1 pt-2 text-[10px] uppercase tracking-wide text-white/35'

/** 分组之间的分隔线。 */
export const PLAYER_MENU_DIVIDER = 'my-1 h-px bg-white/10'

/** 抽屉/面板的标题栏。 */
export const PLAYER_PANEL_HEADER =
  'flex shrink-0 items-center justify-between gap-2 border-b border-white/10 px-4 py-3'

/** 分段选择（播放方式、投影方式这类互斥选项）。 */
export const PLAYER_SEGMENT_GROUP = 'flex items-center gap-1 rounded-lg bg-white/5 p-0.5'
export const PLAYER_SEGMENT = 'flex-1 rounded-md px-2 py-1 text-[11px] font-medium transition'
export const PLAYER_SEGMENT_ON = 'bg-rose-500 text-white'
export const PLAYER_SEGMENT_OFF = 'text-white/70 hover:bg-white/10 hover:text-white'

/** 开关状态的绿色小点（例如弹幕已加载、VR 素材已识别）。 */
export const PLAYER_STATUS_DOT = 'h-1.5 w-1.5 shrink-0 rounded-full'

/**
 * 进度条/音量条上的隐形 range 输入。
 *
 * 视觉部分（轨道、已播放段、圆点手柄）全部自己画，原生 range 只负责三件事：
 * 点击定位、拖拽、键盘与无障碍语义。所以它必须完全透明——但**不能**
 * pointer-events-none，否则拖不动。
 */
export const PLAYER_RANGE_OVERLAY =
  'absolute inset-0 h-full w-full cursor-pointer appearance-none bg-transparent outline-none ' +
  '[&::-webkit-slider-runnable-track]:bg-transparent ' +
  '[&::-webkit-slider-thumb]:h-4 [&::-webkit-slider-thumb]:w-4 [&::-webkit-slider-thumb]:appearance-none ' +
  '[&::-moz-range-track]:bg-transparent ' +
  '[&::-moz-range-thumb]:h-4 [&::-moz-range-thumb]:w-4 [&::-moz-range-thumb]:border-0 ' +
  '[&::-moz-range-thumb]:appearance-none [&::-moz-range-thumb]:bg-transparent'

/** 自绘的细轨道。悬停时整条轨道加粗，是 B 站进度条最直观的那点反馈。 */
export const PLAYER_TRACK =
  'pointer-events-none absolute inset-x-0 h-[3px] rounded-full bg-white/25 transition-[height] duration-150'
export const PLAYER_TRACK_FILL = 'pointer-events-none absolute inset-y-0 left-0 rounded-full'
