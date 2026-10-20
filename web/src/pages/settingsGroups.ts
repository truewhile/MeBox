import { adultSettingsGroup } from './settingsGroupAccess'
import { apiConfigsSettingsGroup } from './settingsGroupAPIConfigs'
import { deviceNotifySettingsGroup } from './settingsGroupDeviceNotify'
import { generalSettingsGroup } from './settingsGroupGeneral'
import { recognitionWordsSettingsGroup } from './settingsGroupRecognitionWords'
import type { SettingGroup } from './settingsGroupTypes'

export type { SettingGroup } from './settingsGroupTypes'

export const databaseSettingsGroup: SettingGroup = {
  key: 'database',
  label: '数据库',
  description: '配置底层数据库（SQLite / PostgreSQL）及数据平滑迁移',
  items: [],
}

export const aboutSettingsGroup: SettingGroup = {
  key: 'about',
  label: '关于',
  description: '项目信息、版本信息与使用指南',
  items: [],
}

export const backupSettingsGroup: SettingGroup = {
  key: 'backup',
  label: '备份',
  description: '创建、下载、恢复和清理数据库备份',
  items: [],
}

export const GROUPS: SettingGroup[] = [
  generalSettingsGroup,
  deviceNotifySettingsGroup,
  databaseSettingsGroup,
  backupSettingsGroup,
  apiConfigsSettingsGroup,
  recognitionWordsSettingsGroup,
  adultSettingsGroup,
  aboutSettingsGroup,
]

export const ALL_KEYS = new Set(GROUPS.flatMap((group) => group.items.map((item) => item.key)))
