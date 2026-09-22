import { api } from './client'

// 设备管理客户端。
//
// 普通用户走 /me/devices（服务端按会话身份过滤，前端无法越权指定他人）；
// 管理员走 /admin/users/:id/devices 代管。

export interface DeviceInfo {
  id: string
  device_id: string
  device_name?: string
  client?: string
  last_ip?: string
  last_seen_at?: string
  last_play_at?: string
  kicked: boolean
  online: boolean
  playing: boolean
  warnings: number
}

interface DeviceListResponse {
  devices: DeviceInfo[] | null
}

export const devicesAPI = {
  listMine: () =>
    api.get<DeviceListResponse>('/me/devices').then((r) => r.data.devices ?? []),

  kickMine: (deviceId: string) =>
    api.post(`/me/devices/${encodeURIComponent(deviceId)}/kick`).then((r) => r.data),

  kickAllMine: () => api.post('/me/devices/kick-all').then((r) => r.data),

  listForUser: (userId: string) =>
    api
      .get<DeviceListResponse>(`/admin/users/${encodeURIComponent(userId)}/devices`)
      .then((r) => r.data.devices ?? []),

  kickForUser: (userId: string, deviceId: string) =>
    api
      .post(
        `/admin/users/${encodeURIComponent(userId)}/devices/${encodeURIComponent(deviceId)}/kick`,
      )
      .then((r) => r.data),

  kickAllForUser: (userId: string) =>
    api.post(`/admin/users/${encodeURIComponent(userId)}/devices/kick-all`).then((r) => r.data),
}
