import { api } from './client'
import type { EmbyMount, EmbyMountInput, RemoteEmbyView } from '../types/emby'

export const embyAPI = {
  listMounts: () => api.get<EmbyMount[]>('/admin/emby/mounts').then((r) => r.data),
  createMounts: (payload: EmbyMountInput) =>
    api.post<{ ok: boolean; created: number }>('/admin/emby/mounts', payload).then((r) => r.data),
  updateMount: (id: string, payload: { name?: string; proxy_play?: boolean; enabled?: boolean }) =>
    api.put<EmbyMount>(`/admin/emby/mounts/${id}`, payload).then((r) => r.data),
  deleteMount: (id: string) => api.delete(`/admin/emby/mounts/${id}`).then((r) => r.data),
  reorderMounts: (ids: string[]) =>
    api.put<{ ok: boolean }>('/admin/emby/mounts/reorder', { ids }).then((r) => r.data),
  setAccountMountsEnabled: (accountId: string, enabled: boolean) =>
    api
      .put<{ ok: boolean; affected: number }>(`/admin/emby/accounts/${accountId}/mounts/enabled`, { enabled })
      .then((r) => r.data),
  listAccountViews: (accountId: string) =>
    api.get<RemoteEmbyView[]>(`/admin/emby/accounts/${accountId}/views`).then((r) => r.data),
  fullMountAccount: (accountId: string, proxy: boolean) =>
    api
      .post<{ ok: boolean; created: number }>(`/admin/emby/accounts/${accountId}/full-mount`, null, {
        params: { proxy: proxy ? 1 : 0 },
      })
      .then((r) => r.data),
}