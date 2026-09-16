import { useCallback, useEffect, useRef, useState } from 'react'
import type { PointerEvent as ReactPointerEvent, RefObject } from 'react'
import { Check, Compass, Minus, Plus, RotateCcw, Settings2, Smartphone } from 'lucide-react'

import {
  VR360_DEFAULT_FOV,
  VR360_MAX_FOV,
  VR360_MIN_FOV,
  VR360_PROJECTION_OPTIONS,
  VR360_STEREO_OPTIONS,
  vr360FrameUv,
  type Vr360Profile,
} from '../utils/vr360'
import {
  applyViewOffset,
  cameraForward,
  cameraFromDeviceOrientation,
  cameraFromYawPitch,
  clampNumber,
  degToRad,
  forwardPitch,
  forwardYaw,
  viewMatrixFromCamera,
  type Mat3,
} from '../utils/vr360Math'
// 只引入类型：渲染器本体走动态 import，普通播放不会下载 WebGL 全景渲染代码。
import type { Vr360Renderer } from '../utils/vr360Renderer'

// 全景播放舞台：一个覆盖整个播放区域的 WebGL 画布。视频元素本身仍在后台正常
// 播放（音轨、进度、弹幕、字幕都不受影响），只是画面被贴到了球面上。
//
// 视角控制：
//   1. 鼠标/手指拖拽 → 转动视角（画面跟随手指），滚轮/双指捏合 → 变焦；
//   2. 手机陀螺仪 → 转动设备即转动视角。iOS 13+ 必须在用户手势里调用
//      DeviceOrientationEvent.requestPermission，所以这里做成显式按钮。

type ViewState = {
  /** 拖拽模式下的方位角（弧度，0 = 默认正前方）。 */
  yaw: number
  /** 拖拽模式下的俯仰角（弧度，+ 为抬头）。 */
  pitch: number
  fov: number
  /** 陀螺仪是否接管视角。 */
  gyroActive: boolean
  /** 最近一次设备姿态对应的相机旋转矩阵。 */
  deviceCamera: Mat3 | null
  gyroYawOffset: number
  gyroPitchOffset: number
}

type PointerState = {
  x: number
  y: number
  startX: number
  startY: number
  startAt: number
  moved: boolean
}

type GyroState = 'off' | 'pending' | 'active' | 'denied' | 'unsupported' | 'unavailable'

type Vr360StageProps = {
  videoRef: RefObject<HTMLVideoElement>
  profile: Vr360Profile
  /** 控制栏是否可见：不可见时隐藏 VR 自身的浮层按钮。 */
  uiVisible: boolean
  /** 轻触画面（未发生拖拽）时回调，与普通播放一致：唤出控制栏或切换播放。 */
  onSurfaceTap: () => void
  onReady: () => void
  onError: (message: string) => void
  /** 用户调整投影方式/立体布局时回调，由播放页保存为下次进入的默认值。 */
  onProfileChange?: (profile: Vr360Profile) => void
}

/** 每像素拖拽对应的转角（弧度）。视场角越小时画面越「拉近」，拖拽也应更细腻。 */
const DRAG_RAD_PER_PIXEL = 0.0035
const TAP_MAX_DISTANCE = 8
const TAP_MAX_DURATION_MS = 400
const GYRO_PROBE_MS = 1500
const PITCH_LIMIT = Math.PI / 2 - 0.03
const WHEEL_ZOOM_SPEED = 0.0015

function screenOrientationAngle(): number {
  if (
    typeof screen !== 'undefined' &&
    screen.orientation &&
    typeof screen.orientation.angle === 'number'
  ) {
    return screen.orientation.angle
  }
  const legacy = (window as unknown as { orientation?: number }).orientation
  return typeof legacy === 'number' ? legacy : 0
}

type DeviceOrientationEventConstructor = {
  requestPermission?: () => Promise<'granted' | 'denied' | 'default'>
}

function gyroStateHint(state: GyroState): string {
  if (state === 'active') return '陀螺仪已开启：转动设备即可改变视角'
  if (state === 'pending') return '正在等待陀螺仪数据…'
  if (state === 'denied') return '陀螺仪权限被拒绝，请在浏览器设置里允许「运动与方向」访问'
  if (state === 'unavailable') return '没有收到陀螺仪数据，当前设备可能不支持'
  if (state === 'unsupported') return '当前浏览器不支持陀螺仪'
  return ''
}

export function Vr360Stage({
  videoRef,
  profile,
  uiVisible,
  onSurfaceTap,
  onReady,
  onError,
  onProfileChange,
}: Vr360StageProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const rendererRef = useRef<Vr360Renderer | null>(null)
  const viewRef = useRef<ViewState>({
    yaw: 0,
    pitch: 0,
    fov: VR360_DEFAULT_FOV,
    gyroActive: false,
    deviceCamera: null,
    gyroYawOffset: 0,
    gyroPitchOffset: 0,
  })
  const dirtyRef = useRef(true)
  const readyRef = useRef(false)
  const pointersRef = useRef(new Map<number, PointerState>())
  const pinchDistanceRef = useRef<number | null>(null)
  const alignmentRef = useRef<{ yaw: number; pitch: number } | null>(null)
  const gyroProbeRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const absoluteSeenRef = useRef(false)
  const profileRef = useRef(profile)
  const [gyroState, setGyroState] = useState<GyroState>('off')
  const [fovHint, setFovHint] = useState(VR360_DEFAULT_FOV)
  const [settingsOpen, setSettingsOpen] = useState(false)

  // 回调放进 ref：渲染器只在挂载时创建一次，不因父组件重渲染而重建。
  const callbacksRef = useRef({ onSurfaceTap, onReady, onError })
  useEffect(() => {
    callbacksRef.current = { onSurfaceTap, onReady, onError }
  }, [onSurfaceTap, onReady, onError])

  const markDirty = useCallback(() => {
    dirtyRef.current = true
  }, [])

  // 当前相机姿态：陀螺仪接管时以设备姿态为基准叠加拖拽偏移，否则纯拖拽角度。
  const currentCamera = useCallback((): Mat3 => {
    const view = viewRef.current
    if (view.gyroActive && view.deviceCamera) {
      return applyViewOffset(view.deviceCamera, view.gyroYawOffset, view.gyroPitchOffset)
    }
    return cameraFromYawPitch(view.yaw, view.pitch)
  }, [])

  const draw = useCallback(() => {
    const renderer = rendererRef.current
    if (!renderer) return
    renderer.setView(viewMatrixFromCamera(currentCamera()))
    renderer.setFov(viewRef.current.fov)
    renderer.draw()
    if (!readyRef.current) {
      // 第一帧真正画出来之后才通知上层：渲染失败时上层会退出 VR 模式。
      readyRef.current = true
      callbacksRef.current.onReady()
    }
  }, [currentCamera])

  const applyProfile = useCallback((renderer: Vr360Renderer, next: Vr360Profile) => {
    renderer.setProjection(next.projection)
    const { scale, offset } = vr360FrameUv(next.stereo)
    renderer.setFrameUv(scale, offset)
    dirtyRef.current = true
  }, [])

  // 创建渲染器（懒加载 WebGL 模块，普通播放不会下载全景渲染代码）。
  useEffect(() => {
    const canvas = canvasRef.current
    const video = videoRef.current
    if (!canvas || !video) return
    let cancelled = false
    let created: Vr360Renderer | null = null

    void import('../utils/vr360Renderer')
      .then((module) => {
        if (cancelled) return
        created = module.createVr360Renderer(canvas, video, {
          onContextLost: () => callbacksRef.current.onError('VR 渲染上下文已丢失，已退出 VR 播放'),
          onTextureError: () => callbacksRef.current.onError('无法读取视频画面用于 VR 渲染'),
        })
        if (!created) {
          callbacksRef.current.onError('当前浏览器不支持 WebGL，无法使用 VR 播放')
          return
        }
        rendererRef.current = created
        const rect = canvas.getBoundingClientRect()
        created.resize(rect.width, rect.height)
        applyProfile(created, profileRef.current)
      })
      .catch(() => {
        if (!cancelled) callbacksRef.current.onError('VR 渲染组件加载失败')
      })

    return () => {
      cancelled = true
      rendererRef.current = null
      created?.dispose()
    }
  }, [applyProfile, videoRef])

  // 投影方式/立体布局变化时重建网格与 UV 裁切。
  useEffect(() => {
    profileRef.current = profile
    const renderer = rendererRef.current
    if (renderer) applyProfile(renderer, profile)
  }, [applyProfile, profile])

  // 画布尺寸跟随舞台（窗口缩放、全屏切换、首次布局）。
  // 观测父容器而不是画布自身：画布尺寸由 CSS 撑满父容器，观测自身会在
  // 「设置绘制缓冲区分辨率 → 布局变化 → 再次触发观测」之间来回放大。
  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return
    const target = canvas.parentElement ?? canvas
    const observer = new ResizeObserver((entries) => {
      const entry = entries[0]
      if (!entry) return
      rendererRef.current?.resize(entry.contentRect.width, entry.contentRect.height)
      markDirty()
    })
    observer.observe(target)
    const rect = target.getBoundingClientRect()
    rendererRef.current?.resize(rect.width, rect.height)
    return () => observer.disconnect()
  }, [markDirty])

  // 渲染循环：requestVideoFrameCallback 标记「有新画面」，rAF 按需绘制。
  // 暂停且无输入时不出图，避免持续占用 GPU。
  useEffect(() => {
    let handle = 0
    let cancelled = false
    const video = videoRef.current as
      | (HTMLVideoElement & { requestVideoFrameCallback?: (callback: () => void) => number })
      | null

    if (video && typeof video.requestVideoFrameCallback === 'function') {
      const onVideoFrame = () => {
        if (cancelled) return
        dirtyRef.current = true
        video.requestVideoFrameCallback?.(onVideoFrame)
      }
      video.requestVideoFrameCallback(onVideoFrame)
    }

    const tick = () => {
      if (cancelled) return
      handle = requestAnimationFrame(tick)
      if (!dirtyRef.current) return
      dirtyRef.current = false
      draw()
    }
    handle = requestAnimationFrame(tick)
    return () => {
      cancelled = true
      cancelAnimationFrame(handle)
    }
  }, [draw, videoRef])

  // 设备姿态 → 相机姿态。第一帧有效数据到达时把视角对齐过去，避免画面跳动。
  const handleOrientation = useCallback((event: DeviceOrientationEvent) => {
    if (event.alpha === null && event.beta === null && event.gamma === null) return
    if (event.type === 'deviceorientationabsolute') absoluteSeenRef.current = true
    // 同时收到绝对与相对事件时以绝对朝向为准（方位角才有意义）。
    else if (absoluteSeenRef.current) return

    const camera = cameraFromDeviceOrientation(
      degToRad(event.alpha ?? 0),
      degToRad(event.beta ?? 0),
      degToRad(event.gamma ?? 0),
      degToRad(screenOrientationAngle()),
    )
    const view = viewRef.current
    view.deviceCamera = camera

    const alignment = alignmentRef.current
    if (alignment) {
      const forward = cameraForward(camera)
      view.gyroYawOffset = alignment.yaw - forwardYaw(forward)
      view.gyroPitchOffset = clampNumber(alignment.pitch - forwardPitch(forward), -1.2, 1.2)
      alignmentRef.current = null
      if (gyroProbeRef.current) {
        clearTimeout(gyroProbeRef.current)
        gyroProbeRef.current = null
      }
      setGyroState('active')
    }

    view.gyroActive = true
    dirtyRef.current = true
  }, [])

  const stopGyro = useCallback(() => {
    window.removeEventListener('deviceorientation', handleOrientation)
    window.removeEventListener('deviceorientationabsolute', handleOrientation)
    if (gyroProbeRef.current) {
      clearTimeout(gyroProbeRef.current)
      gyroProbeRef.current = null
    }
    alignmentRef.current = null
    absoluteSeenRef.current = false
  }, [handleOrientation])

  const startGyro = useCallback(() => {
    alignmentRef.current = { yaw: viewRef.current.yaw, pitch: viewRef.current.pitch }
    absoluteSeenRef.current = false
    window.addEventListener('deviceorientation', handleOrientation)
    window.addEventListener('deviceorientationabsolute', handleOrientation)
    // 桌面浏览器/无陀螺仪设备会一直收不到事件，超时后提示并回退到拖拽模式。
    gyroProbeRef.current = setTimeout(() => {
      gyroProbeRef.current = null
      if (alignmentRef.current) {
        stopGyro()
        setGyroState('unavailable')
      }
    }, GYRO_PROBE_MS)
  }, [handleOrientation, stopGyro])

  const toggleGyro = useCallback(() => {
    if (gyroState === 'active' || gyroState === 'pending') {
      // 关闭陀螺仪时用当前合成视角续上，画面不会跳。
      const forward = cameraForward(currentCamera())
      viewRef.current.yaw = forwardYaw(forward)
      viewRef.current.pitch = clampNumber(forwardPitch(forward), -PITCH_LIMIT, PITCH_LIMIT)
      viewRef.current.gyroActive = false
      viewRef.current.gyroYawOffset = 0
      viewRef.current.gyroPitchOffset = 0
      stopGyro()
      setGyroState('off')
      markDirty()
      return
    }

    const orientationEvent = (
      window as unknown as { DeviceOrientationEvent?: DeviceOrientationEventConstructor }
    ).DeviceOrientationEvent
    if (!orientationEvent) {
      setGyroState('unsupported')
      return
    }
    setGyroState('pending')
    const requestPermission = orientationEvent.requestPermission
    if (typeof requestPermission !== 'function') {
      startGyro()
      return
    }
    // iOS 13+ 必须在用户手势里申请权限，这里由按钮点击直接触发。
    void requestPermission
      .call(orientationEvent)
      .then((result) => {
        if (result === 'granted') startGyro()
        else setGyroState('denied')
      })
      .catch(() => setGyroState('denied'))
  }, [currentCamera, gyroState, markDirty, startGyro, stopGyro])

  useEffect(() => () => stopGyro(), [stopGyro])

  const zoomBy = useCallback(
    (factor: number) => {
      const view = viewRef.current
      view.fov = clampNumber(view.fov * factor, VR360_MIN_FOV, VR360_MAX_FOV)
      setFovHint(view.fov)
      markDirty()
    },
    [markDirty],
  )

  const resetView = useCallback(() => {
    const view = viewRef.current
    view.yaw = 0
    view.pitch = 0
    view.fov = VR360_DEFAULT_FOV
    view.gyroYawOffset = 0
    view.gyroPitchOffset = 0
    setFovHint(view.fov)
    markDirty()
  }, [markDirty])

  const pinchDistance = useCallback((): number => {
    const pointers = [...pointersRef.current.values()]
    if (pointers.length < 2) return 0
    return Math.hypot(pointers[0].x - pointers[1].x, pointers[0].y - pointers[1].y)
  }, [])

  const handlePointerDown = (event: ReactPointerEvent<HTMLCanvasElement>) => {
    try {
      event.currentTarget.setPointerCapture?.(event.pointerId)
    } catch {
      // 某些环境（合成事件、非活跃指针）会抛 InvalidPointerId，退化为普通拖拽即可。
    }
    pointersRef.current.set(event.pointerId, {
      x: event.clientX,
      y: event.clientY,
      startX: event.clientX,
      startY: event.clientY,
      startAt: Date.now(),
      moved: false,
    })
    if (pointersRef.current.size === 2) {
      pinchDistanceRef.current = pinchDistance()
      // 双指手势一律不算轻触，避免捏合缩放时误触发播放/暂停。
      for (const pointer of pointersRef.current.values()) pointer.moved = true
    }
  }

  const handlePointerMove = (event: ReactPointerEvent<HTMLCanvasElement>) => {
    const pointer = pointersRef.current.get(event.pointerId)
    if (!pointer) return
    const dx = event.clientX - pointer.x
    const dy = event.clientY - pointer.y
    pointer.x = event.clientX
    pointer.y = event.clientY
    if (
      Math.hypot(event.clientX - pointer.startX, event.clientY - pointer.startY) > TAP_MAX_DISTANCE
    ) {
      pointer.moved = true
    }

    if (pointersRef.current.size >= 2) {
      const distance = pinchDistance()
      const previous = pinchDistanceRef.current
      pinchDistanceRef.current = distance
      if (previous && distance > 0) zoomBy(previous / distance)
      return
    }

    const view = viewRef.current
    const scale = DRAG_RAD_PER_PIXEL * (view.fov / VR360_DEFAULT_FOV)
    if (view.gyroActive && view.deviceCamera) {
      view.gyroYawOffset -= dx * scale
      view.gyroPitchOffset = clampNumber(
        view.gyroPitchOffset + dy * scale,
        -PITCH_LIMIT,
        PITCH_LIMIT,
      )
    } else {
      view.yaw -= dx * scale
      view.pitch = clampNumber(view.pitch + dy * scale, -PITCH_LIMIT, PITCH_LIMIT)
    }
    markDirty()
  }

  const endPointer = (event: ReactPointerEvent<HTMLCanvasElement>) => {
    const pointer = pointersRef.current.get(event.pointerId)
    pointersRef.current.delete(event.pointerId)
    if (pointersRef.current.size < 2) pinchDistanceRef.current = null
    if (!pointer) return
    const isTap =
      !pointer.moved &&
      Date.now() - pointer.startAt <= TAP_MAX_DURATION_MS &&
      pointersRef.current.size === 0
    if (isTap) onSurfaceTap()
  }

  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return
    const onWheel = (event: WheelEvent) => {
      event.preventDefault()
      zoomBy(1 + event.deltaY * WHEEL_ZOOM_SPEED)
    }
    canvas.addEventListener('wheel', onWheel, { passive: false })
    return () => canvas.removeEventListener('wheel', onWheel)
  }, [zoomBy])

  const hint = gyroStateHint(gyroState)

  return (
    <>
      <canvas
        ref={canvasRef}
        data-vr360-surface
        className="absolute inset-0 h-full w-full cursor-grab touch-none select-none active:cursor-grabbing"
        onPointerDown={handlePointerDown}
        onPointerMove={handlePointerMove}
        onPointerUp={endPointer}
        onPointerCancel={endPointer}
        // 轻触由指针逻辑自己处理（拖动时不能误触发播放/暂停），
        // 同时阻止冒泡到舞台的点击逻辑，避免一次触摸切换两次播放状态。
        onClick={(event) => event.stopPropagation()}
      />
      <div
        className={`pointer-events-none absolute inset-x-0 top-2 z-20 flex flex-col items-center gap-1.5 px-2 transition-opacity duration-300 ${
          uiVisible ? 'opacity-100' : 'opacity-0'
        }`}
      >
        <div className="pointer-events-auto flex flex-wrap items-center justify-center gap-1.5 rounded-2xl border border-white/15 bg-black/65 px-2 py-1.5 text-white shadow-2xl backdrop-blur">
          <button
            type="button"
            onClick={toggleGyro}
            className={`flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium transition ${
              gyroState === 'active'
                ? 'bg-rose-500/90 text-white hover:bg-rose-500'
                : 'bg-white/10 text-white/85 hover:bg-white/20'
            }`}
            title="用手机陀螺仪转动视角"
          >
            <Smartphone size={14} />
            陀螺仪
            {gyroState === 'active' && <Compass size={13} className="text-white/85" />}
          </button>
          <button
            type="button"
            onClick={resetView}
            className="flex items-center gap-1.5 rounded-full bg-white/10 px-2.5 py-1 text-xs font-medium text-white/85 transition hover:bg-white/20"
            title="恢复默认视角与视场角"
          >
            <RotateCcw size={14} />
            重置
          </button>
          <button
            type="button"
            onClick={() => setSettingsOpen((open) => !open)}
            className={`flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium transition ${
              settingsOpen
                ? 'bg-rose-500/90 text-white hover:bg-rose-500'
                : 'bg-white/10 text-white/85 hover:bg-white/20'
            }`}
            title="投影方式与画幅布局"
          >
            <Settings2 size={14} />
            画面
          </button>
          <div className="flex items-center gap-0.5">
            <button
              type="button"
              onClick={() => zoomBy(1.15)}
              className="rounded-full p-1.5 transition hover:bg-white/15"
              title="视野拉远"
            >
              <Minus size={15} />
            </button>
            <span className="w-9 text-center text-[10px] tabular-nums text-white/75">
              {(VR360_DEFAULT_FOV / fovHint).toFixed(1)}×
            </span>
            <button
              type="button"
              onClick={() => zoomBy(1 / 1.15)}
              className="rounded-full p-1.5 transition hover:bg-white/15"
              title="视野拉近"
            >
              <Plus size={15} />
            </button>
          </div>
        </div>
        <p className="pointer-events-none rounded-full bg-black/45 px-3 py-1 text-[10px] text-white/70 backdrop-blur">
          {hint || '拖动旋转视角 · 滚轮或双指缩放'}
        </p>
        {settingsOpen ? (
          <div className="pointer-events-auto w-[min(92vw,420px)] rounded-2xl border border-white/15 bg-black/75 px-3 py-2.5 text-white shadow-2xl backdrop-blur">
            <p className="mb-1.5 text-[10px] uppercase tracking-wide text-white/45">投影方式</p>
            <div className="flex flex-wrap gap-1.5">
              {VR360_PROJECTION_OPTIONS.map(([value, label]) => (
                <button
                  key={value}
                  type="button"
                  onClick={() => onProfileChange?.({ ...profile, projection: value })}
                  className={`flex items-center gap-1 rounded-lg px-2.5 py-1 text-[11px] transition ${
                    profile.projection === value
                      ? 'bg-rose-500/25 text-rose-200'
                      : 'bg-white/5 text-white/75 hover:bg-white/15'
                  }`}
                >
                  {profile.projection === value && <Check size={11} />}
                  {label}
                </button>
              ))}
            </div>
            <p className="mb-1.5 mt-2.5 text-[10px] uppercase tracking-wide text-white/45">
              画幅布局
            </p>
            <div className="flex flex-wrap gap-1.5">
              {VR360_STEREO_OPTIONS.map(([value, label]) => (
                <button
                  key={value}
                  type="button"
                  onClick={() => onProfileChange?.({ ...profile, stereo: value })}
                  className={`flex items-center gap-1 rounded-lg px-2.5 py-1 text-[11px] transition ${
                    profile.stereo === value
                      ? 'bg-rose-500/25 text-rose-200'
                      : 'bg-white/5 text-white/75 hover:bg-white/15'
                  }`}
                >
                  {profile.stereo === value && <Check size={11} />}
                  {label}
                </button>
              ))}
            </div>
            {profile.stereo !== 'mono' && (
              <p className="mt-2 text-[10px] leading-relaxed text-white/45">
                左右/上下并排素材在当前屏幕上只显示左眼画面；真正的双眼立体画面需要 VR
                头显（WebXR）。
              </p>
            )}
          </div>
        ) : null}
      </div>
    </>
  )
}
