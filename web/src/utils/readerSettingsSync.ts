import { readerAPI } from '../api/reader'
import { isAuthenticated } from '../stores/auth'
import {
  applyReaderSettingsProfile,
  readerSettingsPayload,
  useReaderSettingsStore,
} from '../stores/readerSettings'

// 阅读器偏好（主题 / 排版 / 听书 / 书架展示）的跨设备同步。
//
// 服务端是权威来源，localStorage（zustand persist）退化成首屏缓存：
//   - 登录后 hydrateReaderSettings() 拉一次服务端偏好覆盖本地；
//   - 之后本地任何改动防抖回写服务端。
// 该用户还没保存过时服务端返回 null，这时把本地现值推上去「播种」，
// 这样升级前已经在本机调好的设置不会被重置成默认值。

/** 单次回写延迟：把连点字体 +/-、拖滑块这类连续操作合并成一次请求。 */
const PUSH_DEBOUNCE_MS = 600

/** 拉取服务端期间抑制回写，避免把本地旧值当成用户改动推上去。 */
let hydrating = false
let pushTimer: number | null = null
let subscribed = false

function schedulePush(): void {
  if (hydrating || !isAuthenticated()) return
  if (pushTimer !== null) window.clearTimeout(pushTimer)
  pushTimer = window.setTimeout(() => {
    pushTimer = null
    const payload = readerSettingsPayload(useReaderSettingsStore.getState())
    readerAPI.saveReaderSettings(payload).catch(() => undefined)
  }, PUSH_DEBOUNCE_MS)
}

/** 订阅本地偏好变化并防抖回写服务端；重复调用只生效一次。 */
export function startReaderSettingsSync(): void {
  if (subscribed) return
  subscribed = true
  useReaderSettingsStore.subscribe(() => schedulePush())
}

/**
 * 拉取当前账号的阅读器偏好并应用到本地。
 * 服务端异常 / 离线时保持本地值不动，等下一次改动再同步。
 */
export async function hydrateReaderSettings(): Promise<void> {
  if (!isAuthenticated()) return
  hydrating = true
  try {
    const remote = await readerAPI.getReaderSettings()
    if (remote) {
      applyReaderSettingsProfile(remote)
    } else {
      // 该账号还没有偏好记录：用本地现值播种，保证老用户的设置迁移过去
      const payload = readerSettingsPayload(useReaderSettingsStore.getState())
      await readerAPI.saveReaderSettings(payload).catch(() => undefined)
    }
  } catch {
    // 保持本地缓存值，不打扰用户
  } finally {
    hydrating = false
  }
}
