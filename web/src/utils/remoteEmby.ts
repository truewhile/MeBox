// 远程 Emby 挂载条目 / 媒体库的识别与只读标记。
// 与后端 internal/service/emby_remote_ids.go 的 ID 伪装协议保持一致：
// embyremote~{accountID}~{remoteID}。
export function isRemoteEmbyID(id?: string | null): boolean {
  return Boolean(id && id.startsWith('embyremote~'))
}

// partitionPreviewIDs 把本地库和远程库拆开。混在一个预览请求里时，本地 SQL
// 会等最慢的远程 Emby 回来才一起返回。
export function partitionPreviewIDs(ids: string[]): string[][] {
  const local: string[] = []
  const remote: string[] = []
  for (const id of ids) {
    if (isRemoteEmbyID(id)) remote.push(id)
    else local.push(id)
  }
  const batches: string[][] = []
  if (local.length > 0) batches.push(local)
  if (remote.length > 0) batches.push(remote)
  return batches
}