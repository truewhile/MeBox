import { FormEvent } from 'react'
import { Loader2, Plus, Save } from 'lucide-react'

type AdminUsersFormProps = {
  usersCount: number
  maxUsers: number
  maxUsersDraft: string
  savingLimit: boolean
  creating: boolean
  username: string
  password: string
  userLimitReached: boolean
  onMaxUsersDraftChange: (value: string) => void
  onSaveMaxUsers: () => void
  onUsernameChange: (value: string) => void
  onPasswordChange: (value: string) => void
  onSubmit: (e: FormEvent) => void
}

export function AdminUsersForm({
  usersCount,
  maxUsers,
  maxUsersDraft,
  savingLimit,
  creating,
  username,
  password,
  userLimitReached,
  onMaxUsersDraftChange,
  onSaveMaxUsers,
  onUsernameChange,
  onPasswordChange,
  onSubmit,
}: AdminUsersFormProps) {
  const draftValue = Number(maxUsersDraft)
  const limitChanged = maxUsersDraft.trim() !== '' && Number.isFinite(draftValue) && draftValue !== maxUsers

  return (
    <form onSubmit={onSubmit} className="glass-panel grid gap-3 md:grid-cols-[1fr_1fr_auto]">
      <div className="md:col-span-3 flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="font-display text-lg font-semibold text-ink-600">用户管理</h2>
          <p className="text-xs text-sand-500">
            已创建 {usersCount}/{maxUsers} 个用户；新增用户默认只有媒体库浏览、播放、外部播放器与第三方客户端观看权限。
          </p>
        </div>
        <span className="rounded-full border border-primary-400/30 px-3 py-1 text-xs text-brand-500">
          默认管理员不可删除 · 最高权限
        </span>
      </div>

      <div className="md:col-span-3 flex flex-wrap items-end gap-3 rounded-2xl border border-sand-200/80 bg-white/60 p-4">
        <label className="min-w-[10rem] flex-1">
          <span className="mb-1 block text-xs font-medium text-sand-500">单实例用户数量上限</span>
          <input
            type="number"
            min={1}
            max={10000}
            className="input-base"
            value={maxUsersDraft}
            onChange={(e) => onMaxUsersDraftChange(e.target.value)}
          />
        </label>
        <button
          type="button"
          className="neon-button inline-flex items-center justify-center gap-2"
          disabled={!limitChanged || savingLimit || !Number.isFinite(draftValue) || draftValue < 1}
          onClick={onSaveMaxUsers}
        >
          <Save size={16} />
          {savingLimit ? '保存中…' : '保存上限'}
        </button>
        <p className="w-full text-xs text-sand-500">
          默认 20 人。达到上限后无法继续注册或添加用户；若当前用户数已超过新上限，已有账号不受影响，但需删除部分用户后才能继续添加。
        </p>
      </div>

      <input
        required
        className="input-base"
        placeholder="用户名"
        value={username}
        onChange={(e) => onUsernameChange(e.target.value)}
        disabled={userLimitReached}
      />
      <input
        required
        minLength={6}
        className="input-base"
        placeholder="初始密码（至少 6 位）"
        type="password"
        value={password}
        onChange={(e) => onPasswordChange(e.target.value)}
        disabled={userLimitReached}
      />
      <button
        type="submit"
        className="neon-button inline-flex items-center justify-center gap-2 disabled:opacity-50"
        disabled={userLimitReached || creating}
      >
        {creating ? <Loader2 size={16} className="animate-spin" /> : <Plus size={16} />}
        {creating ? '添加中…' : '添加用户'}
      </button>
    </form>
  )
}
