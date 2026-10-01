// VR360 播放用到的纯数学：3x3 旋转矩阵、设备姿态到相机姿态、透视投影。
//
// 坐标约定（与 W3C DeviceOrientation 规范一致，右手系）：
//   x = 东（设备右侧），y = 北（设备顶部），z = 天空（设备屏幕朝外）
//   相机看向相机局部坐标的 -z，设备平放时 -z 指向地面，因此设备平放时视角朝下，
//   竖持手机（beta=90°）时视角水平向前 —— 与手机陀螺仪的实际手感一致。
//
// 所有 3x3 矩阵都是 WebGL 默认的列主序（column-major）Float32Array(9)，
// 可以直接交给 gl.uniformMatrix3fv，不需要转置。

export type Mat3 = Float32Array

export function degToRad(degrees: number): number {
  return (degrees * Math.PI) / 180
}

export function clampNumber(value: number, min: number, max: number): number {
  if (!Number.isFinite(value)) return min
  return Math.min(max, Math.max(min, value))
}

export function createMat3(
  m00: number, m10: number, m20: number,
  m01: number, m11: number, m21: number,
  m02: number, m12: number, m22: number,
): Mat3 {
  // WebGL 列主序内存布局：[col0, col1, col2]
  return new Float32Array([m00, m10, m20, m01, m11, m21, m02, m12, m22])
}

export function identity3(): Mat3 {
  return createMat3(1, 0, 0, 0, 1, 0, 0, 0, 1)
}

export function rotationX(radians: number): Mat3 {
  const c = Math.cos(radians)
  const s = Math.sin(radians)
  return createMat3(1, 0, 0, 0, c, s, 0, -s, c)
}

export function rotationY(radians: number): Mat3 {
  const c = Math.cos(radians)
  const s = Math.sin(radians)
  return createMat3(c, 0, -s, 0, 1, 0, s, 0, c)
}

export function rotationZ(radians: number): Mat3 {
  const c = Math.cos(radians)
  const s = Math.sin(radians)
  return createMat3(c, s, 0, -s, c, 0, 0, 0, 1)
}

/** 先施加 b，再施加 a（即矩阵乘积 a·b）。 */
export function multiply3(a: Mat3, b: Mat3): Mat3 {
  const out = new Float32Array(9)
  for (let col = 0; col < 3; col++) {
    for (let row = 0; row < 3; row++) {
      let sum = 0
      for (let k = 0; k < 3; k++) {
        sum += a[k * 3 + row] * b[col * 3 + k]
      }
      out[col * 3 + row] = sum
    }
  }
  return out
}

/** 旋转矩阵的逆等于转置。 */
export function transpose3(m: Mat3): Mat3 {
  const out = new Float32Array(9)
  for (let col = 0; col < 3; col++) {
    for (let row = 0; row < 3; row++) {
      out[col * 3 + row] = m[row * 3 + col]
    }
  }
  return out
}

export function applyDirection(m: Mat3, x: number, y: number, z: number): [number, number, number] {
  return [
    m[0] * x + m[3] * y + m[6] * z,
    m[1] * x + m[4] * y + m[7] * z,
    m[2] * x + m[5] * y + m[8] * z,
  ]
}

/** 相机在世界坐标中的朝向（相机局部 -z）。 */
export function cameraForward(camera: Mat3): [number, number, number] {
  const forward = applyDirection(camera, 0, 0, -1)
  const length = Math.hypot(forward[0], forward[1], forward[2]) || 1
  return [forward[0] / length, forward[1] / length, forward[2] / length]
}

/** 相机朝向的水平方位角（0 = +y 即正北，顺时针转向 +x 即正东）。 */
export function forwardYaw(forward: [number, number, number]): number {
  return Math.atan2(forward[0], forward[1])
}

/** 相机朝向的俯仰角（+ 抬头，- 低头）。 */
export function forwardPitch(forward: [number, number, number]): number {
  return Math.asin(clampNumber(forward[2], -1, 1))
}

/**
 * 由「用户拖拽/默认视角」的方位角与俯仰角构造相机姿态。
 * 默认（yaw = 0, pitch = 0）时相机水平看向 +y，画面中的地平线保持水平。
 */
export function cameraFromYawPitch(yaw: number, pitch: number): Mat3 {
  return multiply3(rotationZ(-yaw), rotationX(Math.PI / 2 + pitch))
}

/**
 * 由设备姿态构造相机姿态（W3C 规范：R = Rz(α)·Rx(β)·Ry(γ)），
 * 再按屏幕旋转角做一次横滚校正，使画面正向与屏幕正向一致。
 */
export function cameraFromDeviceOrientation(
  alpha: number,
  beta: number,
  gamma: number,
  screenAngle: number,
): Mat3 {
  const device = multiply3(multiply3(rotationZ(alpha), rotationX(beta)), rotationY(gamma))
  return multiply3(device, rotationZ(-screenAngle))
}

/**
 * 在设备姿态之上叠加用户拖拽产生的视角偏移：水平方向围绕世界 z 轴旋转，
 * 垂直方向围绕相机自身的右轴倾斜（小角度下等效于抬头/低头）。
 */
export function applyViewOffset(camera: Mat3, yawOffset: number, pitchOffset: number): Mat3 {
  return multiply3(rotationZ(-yawOffset), multiply3(camera, rotationX(pitchOffset)))
}

/** 世界坐标 → 相机坐标的视图矩阵（纯旋转，所以就是转置）。 */
export function viewMatrixFromCamera(camera: Mat3): Mat3 {
  return transpose3(camera)
}

/** 标准透视投影矩阵（列主序 4x4）。 */
export function perspectiveMatrix(
  fovY: number,
  aspect: number,
  near: number,
  far: number,
): Float32Array {
  const f = 1 / Math.tan(fovY / 2)
  const rangeInv = 1 / (near - far)
  return new Float32Array([
    f / aspect, 0, 0, 0,
    0, f, 0, 0,
    0, 0, (far + near) * rangeInv, -1,
    0, 0, 2 * far * near * rangeInv, 0,
  ])
}
