import { useCallback, useEffect, useRef, useState, type Dispatch, type SetStateAction } from 'react'
import toast from 'react-hot-toast'

import { adminAPI } from '../api/admin'

const SETTING_KEY = 'scrape.episode_images'

// This is an instance-wide scraper preference. The existing admin settings
// endpoint stores it in the Setting table, so it follows the server instead
// of a particular browser.
export function useEpisodeArtworkPreference(): [boolean, Dispatch<SetStateAction<boolean>>] {
  const [enabled, setEnabledState] = useState(false)
  const enabledRef = useRef(false)
  const changedLocally = useRef(false)
  const saveQueue = useRef(Promise.resolve())

  useEffect(() => {
    let active = true
    adminAPI.listSettings()
      .then((settings) => {
        if (!active || changedLocally.current) return
        const value = settings.find((setting) => setting.key === SETTING_KEY)?.value
        const next = value === 'true' || value === '1'
        enabledRef.current = next
        setEnabledState(next)
      })
      .catch(() => {
        if (active) {
          toast.error('读取每集图片设置失败')
        }
      })
    return () => {
      active = false
    }
  }, [])

  const setEnabled = useCallback((nextValue: SetStateAction<boolean>) => {
    const next = typeof nextValue === 'function' ? nextValue(enabledRef.current) : nextValue
    if (enabledRef.current === next) return
    changedLocally.current = true
    enabledRef.current = next
    setEnabledState(next)

    saveQueue.current = saveQueue.current
      .catch(() => undefined)
      .then(() => adminAPI.updateSetting(SETTING_KEY, String(next)))
      .catch(() => {
        toast.error('保存每集图片设置失败')
      })
  }, [])

  return [enabled, setEnabled]
}
