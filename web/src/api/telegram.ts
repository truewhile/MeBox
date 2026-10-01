import { api } from './client'

// Telegram 通知绑定客户端。
//
// 绑定流程：startBind() 拿一次性码 → 用户在 Bot 里发 /bind <码> → 轮询 status()
// 直到 bound 变 true。服务端不接收用户手填的 chat id。

export interface TelegramStatus {
  bound: boolean
  chat_id_masked?: string
}

export interface TelegramBindCode {
  code: string
  expires_in_seconds: number
}

export const telegramAPI = {
  status: () => api.get<TelegramStatus>('/me/telegram').then((r) => r.data),

  startBind: () =>
    api.post<TelegramBindCode>('/me/telegram/bind-code').then((r) => r.data),

  unbind: () => api.delete('/me/telegram').then((r) => r.data),

  test: () => api.post<{ success: boolean; error?: string }>('/admin/telegram/test').then((r) => r.data),
}
