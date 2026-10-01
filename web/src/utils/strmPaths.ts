/** 取路径最后一级目录/文件名（兼容 / 与 \\）。 */
export function lastPathSegment(path: string): string {
  const trimmed = path.trim().replace(/[\\/]+$/, '')
  if (!trimmed) return ''
  const parts = trimmed.split(/[/\\]/).filter(Boolean)
  return parts[parts.length - 1] ?? ''
}

/**
 * 取远端末级目录的展示/拼接名。115 等以 ID 作为 remote_path 的网盘
 * 传入浏览时拿到的目录名 tailName，避免把数字 ID 拼进本地路径。
 */
export function resolveRemoteTailName(remotePath: string, tailName?: string): string {
  const name = tailName?.trim()
  if (name) return name
  return lastPathSegment(remotePath)
}

/** 优先取展示路径（115 浏览/反查得到）的末级目录名，否则取远端路径末段。 */
export function remoteTailNameOf(remotePath: string, displayPath?: string): string {
  const trimmed = displayPath?.trim()
  if (trimmed) return lastPathSegment(trimmed)
  return lastPathSegment(remotePath)
}

function pathSeparator(path: string): '/' | '\\' {
  return path.includes('\\') ? '\\' : '/'
}

function normalizeSlashes(path: string): string {
  return path.replace(/\\/g, '/').replace(/\/+$/, '')
}

/** 去掉 local 末尾与 prevRemoteTail 匹配的自动拼接段。 */
function stripRemoteTail(localPath: string, prevRemoteTail: string): string {
  if (!prevRemoteTail) return localPath.trim()
  const trimmed = localPath.trim()
  if (!trimmed) return ''
  const norm = normalizeSlashes(trimmed)
  const suffix = '/' + prevRemoteTail
  if (norm.endsWith(suffix)) {
    const stripped = norm.slice(0, -suffix.length)
    if (!stripped) return ''
    return stripped.split('/').join(pathSeparator(trimmed))
  }
  if (norm === prevRemoteTail) return ''
  return trimmed
}

/** 将远端最后一级目录名拼到本地输出目录末尾；远端变更时替换旧尾段。tailName 为浏览时拿到的目录名。 */
export function syncLocalPathWithRemote(
  localPath: string,
  remotePath: string,
  prevRemoteTail = '',
  tailName?: string,
): { localPath: string; remoteTail: string } {
  const remoteTail = resolveRemoteTailName(remotePath, tailName)
  const base = stripRemoteTail(localPath, prevRemoteTail)
  if (!remoteTail) {
    return { localPath: base || localPath.trim(), remoteTail: '' }
  }
  if (!base) {
    return { localPath: localPath.trim(), remoteTail }
  }
  const sep = pathSeparator(base)
  const normBase = normalizeSlashes(base)
  if (normBase.endsWith('/' + remoteTail)) {
    return { localPath: base, remoteTail }
  }
  return { localPath: `${base}${sep}${remoteTail}`, remoteTail }
}
