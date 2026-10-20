import { useEffect, useState } from 'react'

// 按设备能力判断「手机/平板」：没有真正的鼠标悬停能力的就是触摸设备。
// 应用里已有同样的做法（PlayerControls / Vr360Stage 的 isHoverlessDevice），
// 这里做成 hook：媒体查询在字体缩放、外接鼠标插拔、chrome 设备模拟时会变化，
// 跟随 change 事件回写，比渲染时读一次更稳。
const HOVERLESS_QUERY = '(hover: none)'

function queryHoverless(): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return false
  return window.matchMedia(HOVERLESS_QUERY).matches
}

/** 当前设备是否是触摸设备（手机/平板）。桌面端（带鼠标）返回 false。 */
export function useIsTouchDevice(): boolean {
  const [touch, setTouch] = useState(queryHoverless)

  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return
    const media = window.matchMedia(HOVERLESS_QUERY)
    const onChange = () => setTouch(media.matches)
    onChange()
    media.addEventListener('change', onChange)
    return () => media.removeEventListener('change', onChange)
  }, [])

  return touch
}
