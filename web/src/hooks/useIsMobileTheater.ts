import { useEffect, useState } from 'react'

const THEATER_MAX_WIDTH = 768

function queryTheater(): boolean {
  if (typeof window === 'undefined') return false
  // 窄屏 + 竖屏才进剧场模式：横屏手机自动回到全屏沉浸式布局。
  return window.innerWidth < THEATER_MAX_WIDTH && window.innerHeight >= window.innerWidth
}

/**
 * 是否使用移动端竖屏剧场布局。
 *
 * 桌面端与横屏手机保持原来的全屏居中播放器；只有竖屏手机进
 * 「视频贴顶 + 下方内容区」的剧场模式，避免 16:9 视频在竖屏里
 * 上下大黑边、控制栏远离画面的问题。
 */
export function useIsMobileTheater(): boolean {
  const [theater, setTheater] = useState(queryTheater)

  useEffect(() => {
    const onChange = () => setTheater(queryTheater())
    window.addEventListener('resize', onChange)
    window.addEventListener('orientationchange', onChange)
    return () => {
      window.removeEventListener('resize', onChange)
      window.removeEventListener('orientationchange', onChange)
    }
  }, [])

  return theater
}
