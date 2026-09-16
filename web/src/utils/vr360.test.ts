import {
  detectVr360Profile,
  normalizeVr360Profile,
  sameVr360Profile,
  vr360FrameUv,
  type Vr360Profile,
} from './vr360.ts'
import {
  applyDirection,
  applyViewOffset,
  cameraForward,
  cameraFromDeviceOrientation,
  cameraFromYawPitch,
  forwardPitch,
  forwardYaw,
  multiply3,
  perspectiveMatrix,
  rotationY,
  rotationZ,
  transpose3,
  viewMatrixFromCamera,
} from './vr360Math.ts'

function check(name: string, condition: boolean) {
  if (!condition) throw new Error(`vr360: ${name}`)
}

function close(a: number, b: number, tolerance = 1e-6): boolean {
  return Math.abs(a - b) <= tolerance
}

function closeAll(a: number[], b: number[], tolerance = 1e-6): boolean {
  return a.length === b.length && a.every((value, index) => close(value, b[index], tolerance))
}

function profileOf(profile: Vr360Profile): string {
  return `${profile.projection}/${profile.stereo}`
}

// ---------- 命名/画幅识别 ----------

const plain = detectVr360Profile({ title: '普通电影', path: '/media/电影/movie.mkv', width: 1920, height: 1080 })
check('普通素材不自动进入 VR', !plain.confident)
check('普通素材默认 360 单眼', profileOf(plain.profile) === 'equirect360/mono')

const pano360 = detectVr360Profile({
  title: '全景演示',
  path: '/media/360/equirect_demo.mp4',
  width: 3840,
  height: 1920,
})
check('360 关键词可识别', pano360.confident && pano360.profile.projection === 'equirect360')
check('2:1 的 360 保持单眼', pano360.profile.stereo === 'mono')

const vr360Sbs = detectVr360Profile({ path: '/media/vr/vr360-sbs-8k.mp4', width: 7680, height: 1920 })
check('4:1 画幅识别为左右并排', vr360Sbs.profile.stereo === 'sbs')

const ou360 = detectVr360Profile({ path: '/media/vr/pano360-ou.mp4', width: 3840, height: 3840 })
check('1:1 的 360 识别为上下并排', ou360.profile.stereo === 'ou')

const fisheye = detectVr360Profile({ path: '/media/vr/abc-fisheye-180.mp4', width: 1920, height: 1920 })
check('鱼眼关键词优先于其它信号', fisheye.profile.projection === 'fisheye180')

const vr180Sbs = detectVr360Profile({
  path: '/media/云下载/sivr-270/sivr-270-1.mp4',
  width: 3840,
  height: 1920,
})
check('编号里的 vr 可识别为 VR180', vr180Sbs.confident && vr180Sbs.profile.projection === 'equirect180')
check('VR180 的 2:1 画幅为左右并排', vr180Sbs.profile.stereo === 'sbs')

const vr180Mono = detectVr360Profile({ path: '/media/vr/vr180-mono.mp4', width: 1920, height: 1920 })
check('VR180 的 1:1 画幅为单眼', vr180Mono.profile.stereo === 'mono')

const vr180Ou = detectVr360Profile({ path: '/media/vr/vr180-tb.mp4', width: 1920, height: 3840 })
check('VR180 的 1:2 画幅为上下并排', vr180Ou.profile.stereo === 'ou')

const topBottom = detectVr360Profile({ path: '/media/vr/vr360-top-bottom.mp4' })
check('top-bottom 关键词为上下并排', topBottom.profile.stereo === 'ou')

// 回归：真实媒体库里出现过的误判（单词里含 vr、分辨率写法含 360）。
const valvrave = detectVr360Profile({
  title: '革命机Valvrave',
  path: '/media/anime/2013/[革命机] Valvrave the Liberator (2013)/01.mkv',
})
check('单词里的 vr 不误判（Valvrave）', !valvrave.confident)
check('误判时仍给出可用的默认配置', profileOf(valvrave.profile) === 'equirect360/mono')

const resolution360p = detectVr360Profile({ path: '/media/movie/movie-360p.mp4', width: 640, height: 360 })
check('360p 分辨率写法不误判', !resolution360p.confident)

const titleWithVrWord = detectVr360Profile({ title: '某动画 VR 特别篇', path: '/media/anime/01.mkv' })
check('标题里的独立 VR 词元可识别', titleWithVrWord.confident)

const ipvr = detectVr360Profile({ path: '/media/云下载/ipvr00192pl/ipvr00192.part2.mp4' })
check('编号形态 ipvr00192 可识别', ipvr.confident && ipvr.profile.projection === 'equirect180')

// ---------- 立体裁切与偏好归一化 ----------

check('单眼不裁切', closeAll(vr360FrameUv('mono').scale, [1, 1]))
check('左右并排取左半张', closeAll(vr360FrameUv('sbs').scale, [0.5, 1]))
check('上下并排取上半张', closeAll(vr360FrameUv('ou').scale, [1, 0.5]))
check('并排画面不做偏移', closeAll(vr360FrameUv('sbs').offset, [0, 0]))

check('未知投影回退到 360 全景', normalizeVr360Profile({ projection: 'nope', stereo: 'nope' }).projection === 'equirect360')
check('未知布局回退到单眼', normalizeVr360Profile({}).stereo === 'mono')
check(
  '相同配置判定相等',
  sameVr360Profile({ projection: 'equirect180', stereo: 'sbs' }, { projection: 'equirect180', stereo: 'sbs' }),
)
check(
  '不同配置判定不等',
  !sameVr360Profile({ projection: 'equirect180', stereo: 'sbs' }, { projection: 'equirect180', stereo: 'ou' }),
)

// ---------- 相机姿态与设备陀螺仪 ----------

const identityForward = cameraForward(new Float32Array([1, 0, 0, 0, 1, 0, 0, 0, 1]))
check('单位姿态朝向地面（设备平放）', closeAll([...identityForward], [0, 0, -1]))

const level = cameraFromYawPitch(0, 0)
check('默认视角水平看向正前方', closeAll([...cameraForward(level)], [0, 1, 0]))

const east = cameraFromYawPitch(Math.PI / 2, 0)
check('方位角 90° 转向正东', closeAll([...cameraForward(east)], [1, 0, 0], 1e-6))

const up = cameraFromYawPitch(0, Math.PI / 2)
check('俯仰角 90° 抬头看天', closeAll([...cameraForward(up)], [0, 0, 1], 1e-6))

const northEast = cameraFromYawPitch(Math.PI / 4, 0)
check('方位角 45° 落在东北方向', close(forwardYaw(cameraForward(northEast)), Math.PI / 4))

const tilted = cameraFromYawPitch(0.3, -0.4)
check('朝向角可反解出方位角', close(forwardYaw(cameraForward(tilted)), 0.3, 1e-6))
check('朝向角可反解出俯仰角', close(forwardPitch(cameraForward(tilted)), -0.4, 1e-6))

check(
  '旋转矩阵转置等于反向旋转',
  closeAll([...transpose3(rotationY(0.7))], [...rotationY(-0.7)], 1e-6),
)
check(
  '正反旋转相乘为单位矩阵',
  closeAll(
    [...multiply3(rotationZ(1.1), rotationZ(-1.1))],
    [1, 0, 0, 0, 1, 0, 0, 0, 1],
    1e-6,
  ),
)

// 视图矩阵 = 相机姿态的逆（纯旋转）
const view = viewMatrixFromCamera(east)
check('视图矩阵为相机矩阵的转置', closeAll([...view], [...transpose3(east)], 1e-6))

// 设备姿态约定（与 W3C 规范一致）：相机看向设备背面，
// 因此设备平放时（alpha 任意、beta = 0）视线朝下。
const flat = cameraFromDeviceOrientation(Math.PI / 3, 0, 0, 0)
check('设备平放时看向地面', closeAll([...cameraForward(flat)], [0, 0, -1], 1e-6))

// 竖持手机（beta = 90°）时视线水平
const portrait = cameraFromDeviceOrientation(0, Math.PI / 2, 0, 0)
check('竖持手机时视线水平', close(cameraForward(portrait)[2], 0, 1e-6))
check('竖持手机时视线朝北', close(forwardYaw(cameraForward(portrait)), 0, 1e-6))

// 横屏（屏幕旋转 90°）时相机「上方向」跟随屏幕
const landscape = cameraFromDeviceOrientation(0, Math.PI / 2, 0, Math.PI / 2)
check('横屏时画面随屏幕转向', closeAll([...applyDirection(landscape, 0, 1, 0)], [1, 0, 0], 1e-6))

// 陀螺仪开启瞬间的视角对齐：叠加偏移后朝向应等于对齐前记录的拖拽视角
const aligned = applyViewOffset(cameraFromDeviceOrientation(0, Math.PI / 2, 0, 0), 0.5, 0.25)
check('叠加偏移后方位角对齐', close(forwardYaw(cameraForward(aligned)), 0.5, 1e-6))
check('叠加偏移后俯仰角对齐', close(forwardPitch(cameraForward(aligned)), 0.25, 1e-6))

// ---------- 透视投影 ----------

const projection = perspectiveMatrix(Math.PI / 2, 2, 0.1, 10)
check('透视矩阵缩放项随视场角变化', close(projection[0], 0.5))
check('透视矩阵写入 w = -1（右手系）', projection[11] === -1)

console.log('vr360.test.ts: ok')
