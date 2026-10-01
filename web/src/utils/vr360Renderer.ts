// 用一段 WebGL 把 <video> 贴到球面/半球面上，实现 360°/180° 全景播放。
//
// 不引入 three.js：整个渲染器只是一段着色器 + 一个网格 + 一张视频纹理，依赖
// 极小，同时也不会把播放页首屏体积撑大（该模块由播放器按需懒加载）。
//
// 视频元素始终是同一个正在播放的 <video>，这里只把它当前的帧采样到球面上，
// 因此 HLS、直连、115 云转码等所有播放方式都能直接复用。

import type { Mat3 } from './vr360Math'
import { perspectiveMatrix } from './vr360Math'
import {
  VR360_DEFAULT_FOV,
  VR360_MAX_FOV,
  VR360_MIN_FOV,
  type Vr360Projection,
} from './vr360'

export type Vr360Renderer = {
  setProjection(projection: Vr360Projection): void
  /** 立体素材裁切：只取左眼所在的半张画面。 */
  setFrameUv(scale: [number, number], offset: [number, number]): void
  setView(view: Mat3): void
  setFov(fovDeg: number): void
  resize(width: number, height: number): void
  draw(): void
  dispose(): void
}

export type Vr360RendererOptions = {
  /** WebGL 上下文丢失（移动端切后台、系统回收 GPU 资源）时回调，由调用方退出 VR 模式。 */
  onContextLost?: () => void
  /** 视频帧无法写入纹理（跨域污染等）时回调。 */
  onTextureError?: (error: unknown) => void
}

const VERTEX_SHADER = `
attribute vec3 aPosition;
attribute vec2 aUv;
uniform mat3 uView;
uniform mat4 uProjection;
uniform vec2 uUvScale;
uniform vec2 uUvOffset;
varying vec2 vUv;
void main() {
  vUv = aUv * uUvScale + uUvOffset;
  gl_Position = uProjection * vec4(uView * aPosition, 1.0);
}
`

const FRAGMENT_SHADER = `
precision mediump float;
uniform sampler2D uTexture;
varying vec2 vUv;
void main() {
  gl_FragColor = texture2D(uTexture, vUv);
}
`

// 球面半径为 1 个单位，相机固定在球心，远近裁剪面据此选取。
const NEAR_PLANE = 0.1
const FAR_PLANE = 10

type Mesh = {
  positions: Float32Array
  uvs: Float32Array
  indices: Uint16Array
  indexCount: number
}

type GridPoint = {
  position: [number, number, number]
  uv: [number, number]
}

function buildGrid(
  columns: number,
  rows: number,
  point: (col: number, row: number) => GridPoint,
): Mesh {
  const positions = new Float32Array((columns + 1) * (rows + 1) * 3)
  const uvs = new Float32Array((columns + 1) * (rows + 1) * 2)
  for (let row = 0; row <= rows; row++) {
    for (let col = 0; col <= columns; col++) {
      const index = row * (columns + 1) + col
      const { position, uv } = point(col, row)
      positions[index * 3] = position[0]
      positions[index * 3 + 1] = position[1]
      positions[index * 3 + 2] = position[2]
      uvs[index * 2] = uv[0]
      uvs[index * 2 + 1] = uv[1]
    }
  }

  const indices = new Uint16Array(columns * rows * 6)
  let cursor = 0
  for (let row = 0; row < rows; row++) {
    for (let col = 0; col < columns; col++) {
      const topLeft = row * (columns + 1) + col
      const topRight = topLeft + 1
      const bottomLeft = topLeft + columns + 1
      const bottomRight = bottomLeft + 1
      indices[cursor++] = topLeft
      indices[cursor++] = bottomLeft
      indices[cursor++] = topRight
      indices[cursor++] = topRight
      indices[cursor++] = bottomLeft
      indices[cursor++] = bottomRight
    }
  }

  return { positions, uvs, indices, indexCount: indices.length }
}

/**
 * 等距柱状投影：水平覆盖 ±span，垂直覆盖 -90°~+90°。
 * u 从 0（左端）到 1（右端），v 从 0（画面顶部）到 1（画面底部）。
 */
function buildEquirectMesh(span: number): Mesh {
  const columns = 96
  const rows = 48
  return buildGrid(columns, rows, (col, row) => {
    const lambda = -span + (2 * span * col) / columns
    const phi = -Math.PI / 2 + (Math.PI * row) / rows
    const cosPhi = Math.cos(phi)
    return {
      position: [Math.sin(lambda) * cosPhi, Math.cos(lambda) * cosPhi, Math.sin(phi)],
      uv: [(lambda + span) / (2 * span), 0.5 - phi / Math.PI],
    }
  })
}

/**
 * 180° 鱼眼（等距投影）：画面中心是正前方，纹理半径 r 与偏角 θ 成正比。
 * 方画幅内切圆之外的区域不参与采样，标准 VR180 鱼眼素材即如此。
 */
function buildFisheyeMesh(): Mesh {
  const radial = 64
  const azimuth = 96
  const thetaMax = Math.PI / 2
  return buildGrid(azimuth, radial, (col, row) => {
    const psi = (2 * Math.PI * col) / azimuth
    const theta = (thetaMax * row) / radial
    const sinTheta = Math.sin(theta)
    const radius = theta / thetaMax
    return {
      position: [sinTheta * Math.cos(psi), Math.cos(theta), sinTheta * Math.sin(psi)],
      uv: [0.5 + 0.5 * radius * Math.cos(psi), 0.5 - 0.5 * radius * Math.sin(psi)],
    }
  })
}

function buildMesh(projection: Vr360Projection): Mesh {
  if (projection === 'fisheye180') return buildFisheyeMesh()
  if (projection === 'equirect180') return buildEquirectMesh(Math.PI / 2)
  return buildEquirectMesh(Math.PI)
}

function compileShader(gl: WebGLRenderingContext, type: number, source: string): WebGLShader | null {
  const shader = gl.createShader(type)
  if (!shader) return null
  gl.shaderSource(shader, source)
  gl.compileShader(shader)
  if (!gl.getShaderParameter(shader, gl.COMPILE_STATUS)) {
    gl.deleteShader(shader)
    return null
  }
  return shader
}

/**
 * 在给定画布上创建全景渲染器；浏览器不支持 WebGL 时返回 null，调用方据此
 * 退回普通播放，而不是给用户一个黑屏。
 */
export function createVr360Renderer(
  canvas: HTMLCanvasElement,
  video: HTMLVideoElement,
  options: Vr360RendererOptions = {},
): Vr360Renderer | null {
  const attributes: WebGLContextAttributes = {
    alpha: false,
    antialias: false,
    depth: false,
    stencil: false,
    premultipliedAlpha: false,
    preserveDrawingBuffer: false,
    powerPreference: 'high-performance',
  }
  const gl = (canvas.getContext('webgl2', attributes) ??
    canvas.getContext('webgl', attributes)) as WebGLRenderingContext | null
  if (!gl) return null

  const vertexShader = compileShader(gl, gl.VERTEX_SHADER, VERTEX_SHADER)
  const fragmentShader = compileShader(gl, gl.FRAGMENT_SHADER, FRAGMENT_SHADER)
  if (!vertexShader || !fragmentShader) return null
  const program = gl.createProgram()
  if (!program) return null
  gl.attachShader(program, vertexShader)
  gl.attachShader(program, fragmentShader)
  gl.linkProgram(program)
  gl.deleteShader(vertexShader)
  gl.deleteShader(fragmentShader)
  if (!gl.getProgramParameter(program, gl.LINK_STATUS)) {
    gl.deleteProgram(program)
    return null
  }

  const positionLocation = gl.getAttribLocation(program, 'aPosition')
  const uvLocation = gl.getAttribLocation(program, 'aUv')
  const viewLocation = gl.getUniformLocation(program, 'uView')
  const projectionLocation = gl.getUniformLocation(program, 'uProjection')
  const uvScaleLocation = gl.getUniformLocation(program, 'uUvScale')
  const uvOffsetLocation = gl.getUniformLocation(program, 'uUvOffset')
  const textureLocation = gl.getUniformLocation(program, 'uTexture')

  const positionBuffer = gl.createBuffer()
  const uvBuffer = gl.createBuffer()
  const indexBuffer = gl.createBuffer()
  const texture = gl.createTexture()
  if (!positionBuffer || !uvBuffer || !indexBuffer || !texture) return null

  gl.bindTexture(gl.TEXTURE_2D, texture)
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE)
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE)
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR)
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR)
  // 先放一张 1x1 黑色占位图，视频帧尚未就绪时也不会出现未定义内容。
  gl.texImage2D(
    gl.TEXTURE_2D,
    0,
    gl.RGBA,
    1,
    1,
    0,
    gl.RGBA,
    gl.UNSIGNED_BYTE,
    new Uint8Array([0, 0, 0, 255]),
  )

  gl.disable(gl.CULL_FACE)
  gl.disable(gl.DEPTH_TEST)
  gl.disable(gl.BLEND)
  gl.clearColor(0, 0, 0, 1)

  let indexCount = 0
  let uvScale: [number, number] = [1, 1]
  let uvOffset: [number, number] = [0, 0]
  let view: Mat3 = new Float32Array([1, 0, 0, 0, 1, 0, 0, 0, 1])
  let fov = VR360_DEFAULT_FOV
  let displayWidth = 1
  let displayHeight = 1
  let disposed = false
  let contextLost = false

  const uploadMesh = (mesh: Mesh) => {
    indexCount = mesh.indexCount
    gl.bindBuffer(gl.ARRAY_BUFFER, positionBuffer)
    gl.bufferData(gl.ARRAY_BUFFER, mesh.positions, gl.STATIC_DRAW)
    gl.bindBuffer(gl.ARRAY_BUFFER, uvBuffer)
    gl.bufferData(gl.ARRAY_BUFFER, mesh.uvs, gl.STATIC_DRAW)
    gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, indexBuffer)
    gl.bufferData(gl.ELEMENT_ARRAY_BUFFER, mesh.indices, gl.STATIC_DRAW)
  }
  uploadMesh(buildMesh('equirect360'))

  const handleContextLost = (event: Event) => {
    // 必须 preventDefault，否则上下文不会被标记为可恢复；这里直接让上层退出 VR。
    event.preventDefault()
    contextLost = true
    options.onContextLost?.()
  }
  canvas.addEventListener('webglcontextlost', handleContextLost)

  return {
    setProjection(projection: Vr360Projection) {
      if (disposed) return
      uploadMesh(buildMesh(projection))
    },
    setFrameUv(scale: [number, number], offset: [number, number]) {
      uvScale = scale
      uvOffset = offset
    },
    setView(next: Mat3) {
      view = next
    },
    setFov(next: number) {
      fov = Math.min(VR360_MAX_FOV, Math.max(VR360_MIN_FOV, next))
    },
    resize(nextWidth: number, nextHeight: number) {
      if (disposed) return
      // 画布位置与尺寸完全交给 CSS（inset-0 + 100%），绘制缓冲区只跟随它。
      // 否则「观测到画布变大 → 设置更大的缓冲区 → 布局再次变大」会指数放大。
      canvas.style.width = '100%'
      canvas.style.height = '100%'
      // 移动端按 DPR 渲染 4K 视频纹理代价很高，上限锁 2 倍。
      const dpr = Math.min(2, Math.max(1, window.devicePixelRatio || 1))
      displayWidth = Math.max(1, nextWidth)
      displayHeight = Math.max(1, nextHeight)
      const pixelWidth = Math.max(1, Math.round(displayWidth * dpr))
      const pixelHeight = Math.max(1, Math.round(displayHeight * dpr))
      if (canvas.width !== pixelWidth) canvas.width = pixelWidth
      if (canvas.height !== pixelHeight) canvas.height = pixelHeight
    },
    draw() {
      if (disposed || contextLost) return
      if (canvas.width < 1 || canvas.height < 1) return

      gl.viewport(0, 0, canvas.width, canvas.height)
      gl.clear(gl.COLOR_BUFFER_BIT)

      if (video.readyState >= 2 && video.videoWidth > 0) {
        try {
          gl.bindTexture(gl.TEXTURE_2D, texture)
          gl.pixelStorei(gl.UNPACK_FLIP_Y_WEBGL, 0)
          gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, video)
        } catch (error) {
          options.onTextureError?.(error)
        }
      }

      gl.useProgram(program)
      gl.uniformMatrix3fv(viewLocation, false, view)
      gl.uniformMatrix4fv(
        projectionLocation,
        false,
        perspectiveMatrix(
          (fov * Math.PI) / 180,
          displayWidth / displayHeight,
          NEAR_PLANE,
          FAR_PLANE,
        ),
      )
      gl.uniform2f(uvScaleLocation, uvScale[0], uvScale[1])
      gl.uniform2f(uvOffsetLocation, uvOffset[0], uvOffset[1])
      gl.uniform1i(textureLocation, 0)

      gl.activeTexture(gl.TEXTURE0)
      gl.bindTexture(gl.TEXTURE_2D, texture)

      gl.bindBuffer(gl.ARRAY_BUFFER, positionBuffer)
      gl.enableVertexAttribArray(positionLocation)
      gl.vertexAttribPointer(positionLocation, 3, gl.FLOAT, false, 0, 0)

      gl.bindBuffer(gl.ARRAY_BUFFER, uvBuffer)
      gl.enableVertexAttribArray(uvLocation)
      gl.vertexAttribPointer(uvLocation, 2, gl.FLOAT, false, 0, 0)

      gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, indexBuffer)
      gl.drawElements(gl.TRIANGLES, indexCount, gl.UNSIGNED_SHORT, 0)
    },
    dispose() {
      if (disposed) return
      disposed = true
      canvas.removeEventListener('webglcontextlost', handleContextLost)
      gl.bindBuffer(gl.ARRAY_BUFFER, null)
      gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, null)
      gl.deleteBuffer(positionBuffer)
      gl.deleteBuffer(uvBuffer)
      gl.deleteBuffer(indexBuffer)
      gl.deleteTexture(texture)
      gl.deleteProgram(program)
      // 主动释放上下文：移动端同时存活多个 WebGL 上下文会被浏览器强制回收。
      gl.getExtension('WEBGL_lose_context')?.loseContext()
    },
  }
}
