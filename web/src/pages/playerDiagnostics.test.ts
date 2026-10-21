import { shouldShowPlayerDiagnostics } from './playerPageModel.ts'

function check(name: string, actual: boolean, expected: boolean) {
  if (actual !== expected) throw new Error(`playerDiagnostics: ${name}`)
}

check('hidden by default and during normal playback', shouldShowPlayerDiagnostics('', ''), false)
check('visible when playback fails', shouldShowPlayerDiagnostics('视频播放失败', ''), true)
check('visible when media cannot be loaded', shouldShowPlayerDiagnostics('', '无法加载该媒体'), true)
check('visible when both loading and playback fail', shouldShowPlayerDiagnostics('视频播放失败', '无法加载该媒体'), true)
check('hidden again after errors are cleared', shouldShowPlayerDiagnostics('', ''), false)

console.log('playerDiagnostics.test.ts ok')
