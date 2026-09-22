import {
  FolderLock,
  KeyRound,
  Loader2,
  MonitorSmartphone,
  Pencil,
  ShieldCheck,
  Trash2,
  UserCheck,
  UserX,
  X,
} from 'lucide-react'

import type { User } from '../types'

type AdminUsersTableProps = {
  users: User[]
  editingID: string | null
  editingUsername: string
  resettingPasswordID: string | null
  onEditingUsernameChange: (value: string) => void
  onSaveEdit: (id: string) => void
  onCancelEdit: () => void
  onStartEdit: (user: User) => void
  onResetPassword: (user: User) => void
  onConfigureLibraries: (user: User) => void
  onManageDevices: (user: User) => void
  onToggleStatus: (user: User) => void
  onDeleteUser: (user: User) => void
}

export function AdminUsersTable({
  users,
  editingID,
  editingUsername,
  resettingPasswordID,
  onEditingUsernameChange,
  onSaveEdit,
  onCancelEdit,
  onStartEdit,
  onResetPassword,
  onConfigureLibraries,
  onManageDevices,
  onToggleStatus,
  onDeleteUser,
}: AdminUsersTableProps) {
  return (
    <div className="glass-panel table-scroll">
      <table className="min-w-[900px] w-full text-left text-sm">
        <thead className="text-xs uppercase tracking-wider text-sand-500">
          <tr>
            <th className="py-2 whitespace-nowrap">用户名</th>
            <th className="whitespace-nowrap">角色</th>
            <th className="whitespace-nowrap">媒体库权限</th>
            <th className="whitespace-nowrap">状态</th>
            <th>权限说明</th>
            <th className="whitespace-nowrap">最近登录</th>
            <th className="whitespace-nowrap text-right">操作</th>
          </tr>
        </thead>
        <tbody>
          {users.map((u) => (
            <tr key={u.id} id={`admin-user-${u.id}`} className="border-t border-gray-200">
              <td className="py-2 text-ink-600">
                {editingID === u.id ? (
                  <input
                    className="input-base h-9 max-w-48"
                    value={editingUsername}
                    onChange={(e) => onEditingUsernameChange(e.target.value)}
                  />
                ) : (
                  <span className="inline-flex items-center gap-2">
                    {u.username}
                    {u.is_default_admin && <ShieldCheck size={15} className="text-brand-500" />}
                  </span>
                )}
              </td>
              <td className="text-ink-100 whitespace-nowrap">{u.role === 'admin' ? '管理员' : '观看用户'}</td>
              <td className="whitespace-nowrap">
                {u.role === 'admin' ? (
                  <span className="inline-flex items-center rounded-full bg-sand-100 px-2.5 py-0.5 text-xs text-sand-600 font-medium">
                    全库 (管理员)
                  </span>
                ) : !u.allowed_library_ids || u.allowed_library_ids.length === 0 ? (
                  <span className="inline-flex items-center rounded-full bg-green-50 border border-green-200/80 px-2.5 py-0.5 text-xs font-medium text-green-700">
                    全部媒体库 (默认)
                  </span>
                ) : (
                  <button
                    type="button"
                    onClick={() => onConfigureLibraries(u)}
                    className="inline-flex items-center gap-1 rounded-full bg-brand-50 border border-brand-300 px-2.5 py-0.5 text-xs font-semibold text-brand-700 hover:bg-brand-100/70 transition-colors"
                    title="点击修改媒体库访问权限"
                  >
                    <FolderLock size={12} />
                    <span>已指定 {u.allowed_library_ids.length} 个库</span>
                  </button>
                )}
              </td>
              <td className={`whitespace-nowrap ${u.is_active ? 'text-green-500' : 'text-red-400'}`}>
                {u.is_active ? '正常' : '已禁用'}
              </td>
              <td className="text-ink-50 max-w-[22rem]">
                {u.role === 'admin' ? '全部管理权限' : '仅浏览/播放/外部播放器，无下载与文件操作'}
              </td>
              <td className="text-ink-50 whitespace-nowrap">
                <span className="inline-flex flex-wrap items-center gap-2">
                  <span>{u.last_login_at ? new Date(u.last_login_at).toLocaleString() : '从未登录'}</span>
                  {u.realtime_online && <span className="rounded border border-green-400/40 px-1.5 py-0.5 text-[11px] text-green-500">在线</span>}
                  {(u.realtime_device_count ?? 0) > 0 && <span className="text-xs text-ink-50">{u.realtime_device_count} 台</span>}
                </span>
              </td>
              <td className="space-x-2 py-2 text-right">
                {editingID === u.id ? (
                  <>
                    <button
                      className="rounded-lg border border-primary-400/40 px-2 py-1 text-xs text-brand-500 hover:bg-primary-400/10"
                      onClick={() => onSaveEdit(u.id)}
                    >
                      保存
                    </button>
                    <button
                      className="rounded-lg border border-gray-300 px-2 py-1 text-xs text-ink-100 hover:bg-gray-100"
                      onClick={onCancelEdit}
                    >
                      <X size={12} />
                    </button>
                  </>
                ) : (
                  <button
                    className="rounded-lg border border-primary-400/40 px-2 py-1 text-xs text-brand-500 hover:bg-primary-400/10"
                    title="重命名用户"
                    onClick={() => onStartEdit(u)}
                  >
                    <Pencil size={12} />
                  </button>
                )}
                <button
                  className="rounded-lg border border-brand-400/40 px-2 py-1 text-xs text-brand-600 hover:bg-brand-400/10"
                  title="配置媒体库访问权限"
                  onClick={() => onConfigureLibraries(u)}
                >
                  <FolderLock size={12} />
                </button>
                <button
                  className="rounded-lg border border-amber-400/40 px-2 py-1 text-xs text-amber-500 hover:bg-amber-400/10"
                  title="重置密码"
                  disabled={resettingPasswordID === u.id}
                  onClick={() => onResetPassword(u)}
                >
                  {resettingPasswordID === u.id ? <Loader2 size={12} className="animate-spin" /> : <KeyRound size={12} />}
                </button>
                <button
                  className="rounded-lg border border-sky-400/40 px-2 py-1 text-xs text-sky-500 hover:bg-sky-400/10"
                  title="登录设备管理"
                  onClick={() => onManageDevices(u)}
                >
                  <MonitorSmartphone size={12} />
                </button>
                <button
                  className={
                    'rounded-lg border px-2 py-1 text-xs disabled:cursor-not-allowed disabled:opacity-40 ' +
                    (u.is_active
                      ? 'border-orange-400/40 text-orange-500 hover:bg-orange-400/10'
                      : 'border-green-400/40 text-green-500 hover:bg-green-400/10')
                  }
                  disabled={u.is_protected && u.is_active}
                  title={u.is_active ? '禁用用户' : '解禁用户'}
                  onClick={() => onToggleStatus(u)}
                >
                  {u.is_active ? <UserX size={12} /> : <UserCheck size={12} />}
                </button>
                <button
                  className="rounded-lg border border-red-400/40 px-2 py-1 text-xs text-red-400 hover:bg-red-400/10 disabled:cursor-not-allowed disabled:opacity-40"
                  disabled={u.is_protected}
                  title={u.is_protected ? '默认管理员禁止删除' : '删除用户'}
                  onClick={() => onDeleteUser(u)}
                >
                  <Trash2 size={12} />
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
