import { api, LONG_REQUEST_TIMEOUT } from './client'

export interface BackupInfo {
  filename: string
  size: number
  created_at: string
  automatic: boolean
}

export const backupAPI = {
  list: () => api.get<{ items: BackupInfo[] }>('/admin/backups').then((r) => r.data.items ?? []),
  create: () => api.post<BackupInfo>('/admin/backups', {}, { timeout: LONG_REQUEST_TIMEOUT }).then((r) => r.data),
  remove: (filename: string) => api.delete('/admin/backups', { params: { filename } }),
  restore: (filename: string) => api.post<{ message: string; restart_required: boolean }>('/admin/backups/restore', null, { params: { filename }, timeout: LONG_REQUEST_TIMEOUT }).then((r) => r.data),
  download: async (filename: string) => {
    const response = await api.get<Blob>('/admin/backups/download', {
      params: { filename },
      responseType: 'blob',
      timeout: LONG_REQUEST_TIMEOUT,
    })
    const url = URL.createObjectURL(response.data)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = filename
    anchor.click()
    URL.revokeObjectURL(url)
  },
}
