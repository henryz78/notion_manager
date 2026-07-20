import React, { createContext, useContext, useState, useEffect, useCallback } from 'react'
import { zh } from './zh'
import { en } from './en'
import type { Translations } from './zh'

export type Lang = 'zh' | 'en'

const translations: Record<Lang, Translations> = { zh, en }

// Helper type to traverse nested object keys: e.g. "header.tabAccounts"
type NestedKeys<T> = T extends object
  ? { [K in keyof T & string]: `${K}.${NestedKeys<T[K]>}` | `${K}` }[keyof T & string]
  : never

export type TranslationKey = NestedKeys<Translations>

// Deep getter for nested keys like "header.tabAccounts"
function getNestedValue(obj: any, path: string): string {
  const parts = path.split('.')
  let curr = obj
  for (const part of parts) {
    if (curr && typeof curr === 'object' && part in curr) {
      curr = curr[part]
    } else {
      return path // fallback to key name
    }
  }
  return typeof curr === 'string' ? curr : path
}

// Variable interpolation helper: tFmt("Welcome {name}", { name: "Alice" }) -> "Welcome Alice"
export function formatString(str: string, vars?: Record<string, string | number>): string {
  if (!vars) return str
  return str.replace(/\{(\w+)\}/g, (_, key) => (key in vars ? String(vars[key]) : `{${key}}`))
}

interface LanguageContextType {
  lang: Lang
  setLang: (lang: Lang) => void
  t: (key: TranslationKey, vars?: Record<string, string | number>) => string
  locale: string
}

const LanguageContext = createContext<LanguageContextType | null>(null)

const STORAGE_KEY = 'nm_lang'

export function LanguageProvider({ children }: { children: React.ReactNode }) {
  const [lang, setLangState] = useState<Lang>(() => {
    const saved = localStorage.getItem(STORAGE_KEY)
    return saved === 'en' ? 'en' : 'zh'
  })

  const setLang = useCallback((nextLang: Lang) => {
    setLangState(nextLang)
    localStorage.setItem(STORAGE_KEY, nextLang)
  }, [])

  const t = useCallback(
    (key: TranslationKey, vars?: Record<string, string | number>): string => {
      const dict = translations[lang] || translations.zh
      const raw = getNestedValue(dict, key)
      return formatString(raw, vars)
    },
    [lang]
  )

  const locale = lang === 'zh' ? 'zh-CN' : 'en-US'

  return (
    <LanguageContext.Provider value={{ lang, setLang, t, locale }}>
      {children}
    </LanguageContext.Provider>
  )
}

export function useT() {
  const context = useContext(LanguageContext)
  if (!context) {
    throw new Error('useT must be used within a LanguageProvider')
  }
  return context
}
