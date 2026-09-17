import { ArrowLeft, Gauge, RefreshCw, Sparkles } from 'lucide-react'

import type { PlayerMode } from './playerPageModel'
import { PLAYER_ICON_BUTTON } from '../components/playerTheme'

// PlayerTopBar — 播放器顶部的极简浮层。
//
// 以前这里只有「返回」和一个播放方式药丸按钮，用户看不到自己在看什么；而播放方式
// 又要和控制栏里的画质/倍速混在一起点。现在改成 B 站那样：左边是返回图标 + 当前
// 媒体标题，右边只留一个只读的播放方式状态标签（真正的切换入口收进了控制栏的
// 「设置」面板），顶栏因此不再参与「一堆药丸按钮」的视觉噪音。

type PlayerTopBarProps = {
  /** 当前播放的媒体标题。 */
  title?: string
  /** 标题下的次要信息（集数、版本等）。 */
  subtitle?: string
  directOnly: boolean
  isDirectStream?: boolean
  directStreamLabel?: string
  mode: PlayerMode
  onBack: () => void
}

export function PlayerTopBar({
  title,
  subtitle,
  directOnly,
  isDirectStream,
  directStreamLabel,
  mode,
  onBack,
}: PlayerTopBarProps) {
  const status = isDirectStream
    ? {
        icon: <Sparkles size={12} />,
        label: directStreamLabel || '直连播放',
        title: directStreamLabel
          ? `${directStreamLabel}，默认直连播放，不进行转码`
          : '远程直连播放，不进行转码',
      }
    : directOnly
      ? {
          icon: <Sparkles size={12} />,
          label: '客户端直连解码',
          title: '宿主机不转码，由客户端本地解码直连',
        }
      : mode === 'hls'
        ? {
            icon: <RefreshCw size={12} />,
            label: 'HLS 转码',
            title: '当前为 HLS 转码播放，可在控制栏「设置」里切回直接播放',
          }
        : {
            icon: <Gauge size={12} />,
            label: '直接播放',
            title: '当前直接播放原始文件，可在控制栏「设置」里切到 HLS 转码',
          }

  return (
    <div className="pointer-events-none absolute inset-x-0 top-0 z-20 flex items-start justify-between gap-3 bg-gradient-to-b from-black/60 via-black/20 to-transparent px-2.5 pb-10 pt-2.5 sm:px-3.5 sm:pt-3.5">
      <div className="flex min-w-0 items-center gap-2">
        <button
          onClick={onBack}
          className={`${PLAYER_ICON_BUTTON} pointer-events-auto h-8 w-8 bg-black/35 backdrop-blur hover:bg-black/60 sm:h-9 sm:w-9`}
          title="返回 (Esc)"
        >
          <ArrowLeft size={18} />
        </button>
        {title ? (
          <div className="min-w-0">
            <p className="truncate text-sm font-medium text-white/95 drop-shadow-[0_1px_4px_rgba(0,0,0,0.8)]">
              {title}
            </p>
            {subtitle ? (
              <p className="truncate text-[11px] text-white/65 drop-shadow-[0_1px_4px_rgba(0,0,0,0.8)]">
                {subtitle}
              </p>
            ) : null}
          </div>
        ) : null}
      </div>

      <span
        className="pointer-events-auto flex shrink-0 items-center gap-1.5 rounded-md border border-white/10 bg-black/40 px-2 py-1 text-[11px] font-medium text-white/80 backdrop-blur"
        title={status.title}
      >
        {status.icon}
        {status.label}
      </span>
    </div>
  )
}
