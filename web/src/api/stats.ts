import { api } from './client'

// 统计相关客户端。
//
// 个人统计走 /watch-history/stats（服务端按会话身份取数）；全站排行走
// /stats/top-users，服务端仅允许管理员访问。

export interface TopUserEntry {
  user_id: string
  username: string
  plays: number
}

interface TopUsersResponse {
  items: TopUserEntry[] | null
}

export const statsAPI = {
  topUsers: (limit = 5) =>
    api
      .get<TopUsersResponse>(`/stats/top-users?limit=${limit}`)
      .then((r) => r.data.items ?? []),
}
