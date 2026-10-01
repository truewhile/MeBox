import { api } from './client'
import type {
  StrmAccount,
  StrmAccountInput,
  StrmQueueSnapshot,
  StrmSettingsMap,
  StrmSyncPath,
  StrmSyncPathInput,
  StrmSyncRecord,
} from '../types/strm'

export interface StrmRemoteEntry {
  id: string
  name: string
  is_dir: boolean
  size: number
  pick_code?: string
}

export interface Strm115Source {
  auth_source_type: 'built_in_appid' | 'built_in_relay' | 'third_party_service' | 'custom_appid'
  auth_provider: string
  app_id: string
  app_name: string
  display_name: string
  auth_server?: string
  requires_encryption_key?: boolean
  deprecated?: boolean
}

export interface StrmLocalDirList {
  roots: boolean
  parent?: string
  current?: string
  children: { name: string; path: string }[]
}

export interface Strm115Sources {
  built_in: Strm115Source[]
  relay: Strm115Source[]
  third_party: Strm115Source[]
}

export interface Strm115OAuthStartResult {
  session_id: string
  mode: 'qrcode' | 'url'
  auth_url?: string
  state?: string
  expires_in?: number
  qrcode?: {
    uid: string
    time: number
    sign: string
    qrcode: string
  }
}

export interface Strm115OAuthStatus {
  status: 'waiting' | 'scanned' | 'confirmed' | 'expired'
  tip: string
}

export const strmAPI = {
  // ── 网盘账号 ────────────────────────────────────────────
  listAccounts: () => api.get<StrmAccount[]>('/admin/strm/accounts').then((r) => r.data),

  createAccount: (payload: StrmAccountInput) =>
    api.post<StrmAccount>('/admin/strm/accounts', payload).then((r) => r.data),

  updateAccount: (id: string, payload: StrmAccountInput) =>
    api.put<StrmAccount>(`/admin/strm/accounts/${id}`, payload).then((r) => r.data),

  deleteAccount: (id: string) => api.delete(`/admin/strm/accounts/${id}`).then((r) => r.data),

  testAccount: (id: string) =>
    api.post<StrmAccount>(`/admin/strm/accounts/${id}/test`).then((r) => r.data),

  listRemoteDir: (accountId: string, dir: string) =>
    api
      .get<StrmRemoteEntry[]>(`/admin/strm/accounts/${accountId}/list`, { params: { dir } })
      .then((r) => r.data),

  // 按远端目录引用（115 为目录 ID）反查完整展示路径
  resolveRemoteDirPath: (accountId: string, dir: string) =>
    api
      .get<{ path: string }>(`/admin/strm/accounts/${accountId}/resolve`, { params: { dir } })
      .then((r) => r.data.path),

  list115Sources: () =>
    api.get<Strm115Sources>('/admin/strm/115/sources').then((r) => r.data),

  start115OAuth: (accountId: string, payload: { auth_source: string; app_id?: string; provider?: string; redirect_url?: string }) =>
    api.post<Strm115OAuthStartResult>(`/admin/strm/accounts/${accountId}/oauth/start`, payload).then((r) => r.data),

  poll115OAuth: (accountId: string, sessionId: string) =>
    api.post<Strm115OAuthStatus>(`/admin/strm/accounts/${accountId}/oauth/poll`, { session_id: sessionId }).then((r) => r.data),

  // ── 全局设置 ────────────────────────────────────────────
  getSettings: () =>
    api.get<StrmSettingsMap>('/admin/strm/settings').then((r) => r.data),

  updateSettings: (payload: Partial<StrmSettingsMap>) =>
    api.put('/admin/strm/settings', payload).then((r) => r.data),

  // ── 同步目录 ────────────────────────────────────────────
  listPaths: () => api.get<StrmSyncPath[]>('/admin/strm/paths').then((r) => r.data),

  createPath: (payload: StrmSyncPathInput) =>
    api.post<StrmSyncPath>('/admin/strm/paths', payload).then((r) => r.data),

  updatePath: (id: string, payload: StrmSyncPathInput) =>
    api.put<StrmSyncPath>(`/admin/strm/paths/${id}`, payload).then((r) => r.data),

  deletePath: (id: string) => api.delete(`/admin/strm/paths/${id}`).then((r) => r.data),

  startSync: (id: string, mode: 'incremental' | 'full' = 'incremental') =>
    api.post(`/admin/strm/paths/${id}/sync`, null, { params: { mode } }).then((r) => r.data),

  cancelSync: (id: string) => api.post(`/admin/strm/paths/${id}/cancel`).then((r) => r.data),

  listRecords: (pathId?: string) =>
    api
      .get<StrmSyncRecord[]>('/admin/strm/records', { params: pathId ? { path_id: pathId } : {} })
      .then((r) => r.data),

  deleteRecord: (id: string) => api.delete(`/admin/strm/records/${id}`).then((r) => r.data),

  clearRecords: (pathId?: string) =>
    api
      .delete<{ deleted: number }>('/admin/strm/records', { params: pathId ? { path_id: pathId } : {} })
      .then((r) => r.data),

  // ── 本地目录浏览（同步目录选择器） ────────────────────────
  listLocalDirs: (path?: string) =>
    api
      .get<StrmLocalDirList>('/admin/strm/local-dirs', { params: path ? { path } : {} })
      .then((r) => r.data),

  // ── 下载/上传队列 ───────────────────────────────────────
  downloads: (status?: string, page = 1, pageSize = 50) =>
    api
      .get<StrmQueueSnapshot>('/admin/strm/downloads', {
        params: { status, page, page_size: pageSize },
      })
      .then((r) => r.data),

  cancelDownload: (id: string) =>
    api.post(`/admin/strm/downloads/${id}/cancel`).then((r) => r.data),

  retryDownload: (id: string) =>
    api.post(`/admin/strm/downloads/${id}/retry`).then((r) => r.data),

  deleteDownload: (id: string) =>
    api.delete(`/admin/strm/downloads/${id}`).then((r) => r.data),

  batchActionDownloads: (action: 'delete' | 'retry' | 'cancel', ids: string[]) =>
    api.post<{ affected: number; action: string }>('/admin/strm/downloads/batch', { action, ids }).then((r) => r.data),

  clearDoneDownloads: () =>
    api.post<{ deleted: number }>('/admin/strm/downloads/clear-done').then((r) => r.data),

  clearFinishedDownloads: () =>
    api.post<{ deleted: number }>('/admin/strm/downloads/clear-finished').then((r) => r.data),

  clearCanceledDownloads: () =>
    api.post<{ deleted: number }>('/admin/strm/downloads/clear-canceled').then((r) => r.data),

  clearFailedDownloads: () =>
    api.post<{ deleted: number }>('/admin/strm/downloads/clear-failed').then((r) => r.data),

  retryFailedDownloads: () =>
    api.post<{ retried: number }>('/admin/strm/downloads/retry-failed').then((r) => r.data),

  cancelPendingDownloads: () =>
    api.post<{ canceled: number }>('/admin/strm/downloads/cancel-pending').then((r) => r.data),

  uploads: (status?: string, page = 1, pageSize = 50) =>
    api
      .get<StrmQueueSnapshot>('/admin/strm/uploads', {
        params: { status, page, page_size: pageSize },
      })
      .then((r) => r.data),

  cancelUpload: (id: string) =>
    api.post(`/admin/strm/uploads/${id}/cancel`).then((r) => r.data),

  retryUpload: (id: string) =>
    api.post(`/admin/strm/uploads/${id}/retry`).then((r) => r.data),

  deleteUpload: (id: string) =>
    api.delete(`/admin/strm/uploads/${id}`).then((r) => r.data),

  batchActionUploads: (action: 'delete' | 'retry' | 'cancel', ids: string[]) =>
    api.post<{ affected: number; action: string }>('/admin/strm/uploads/batch', { action, ids }).then((r) => r.data),

  cancelPendingUploads: () =>
    api.post<{ canceled: number }>('/admin/strm/uploads/cancel-pending').then((r) => r.data),

  clearDoneUploads: () =>
    api.post<{ deleted: number }>('/admin/strm/uploads/clear-done').then((r) => r.data),

  clearFinishedUploads: () =>
    api.post<{ deleted: number }>('/admin/strm/uploads/clear-finished').then((r) => r.data),

  clearCanceledUploads: () =>
    api.post<{ deleted: number }>('/admin/strm/uploads/clear-canceled').then((r) => r.data),

  clearFailedUploads: () =>
    api.post<{ deleted: number }>('/admin/strm/uploads/clear-failed').then((r) => r.data),

  retryFailedUploads: () =>
    api.post<{ retried: number }>('/admin/strm/uploads/retry-failed').then((r) => r.data),
}