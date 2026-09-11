import { api } from './client'
import type { User } from '../types'

export const profileAPI = {
  get: () => api.get<User>('/me').then((r) => r.data),

  update: (patch: {
    username?: string
    nickname?: string
    email?: string
    avatar_url?: string
    hide_adult?: boolean
    subtitle_chinese_mode?: 'original' | 'simplified' | 'traditional'
    password?: string
  }) =>
    api.patch<User>('/me', patch).then((r) => r.data),

  getPinnedLibraries: () =>
    api.get<{ library_ids: string[] }>('/me/pinned-libraries').then((r) => r.data.library_ids ?? []),

  setPinnedLibraries: (libraryIds: string[]) =>
    api.put<{ library_ids: string[] }>('/me/pinned-libraries', { library_ids: libraryIds }).then((r) => r.data.library_ids ?? []),

  adminUpdateRole: (id: string, role: 'admin' | 'user') =>
    api.patch<User>(`/admin/users/${id}/role`, { role }).then((r) => r.data),
}
