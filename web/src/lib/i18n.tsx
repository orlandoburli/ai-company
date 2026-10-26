"use client";

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useSyncExternalStore,
} from "react";
import {
  detectLang,
  setCurrentLang,
  translate,
  writeLangCookie,
  type Lang,
  type TranslationKey,
  type TranslateValues,
} from "./locales";

/**
 * The language of the interface.
 *
 * The language lives in a tiny store outside React so that the shared helpers
 * (status labels, date formats) read the same value the screen is drawn in.
 * The provider subscribes to that store: switching the language redraws every
 * consumer at once, with no reload and no lost state.
 */

let resolved: Lang | null = null;
const listeners = new Set<() => void>();

/** The stored choice, the cookie, then the browser's language. */
function resolve(): Lang {
  if (!resolved) resolved = detectLang();
  return resolved;
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function getSnapshot(): Lang {
  const lang = resolve();
  // Keep the shared helpers in step with what is about to be drawn.
  setCurrentLang(lang);
  return lang;
}

/** The server has no cookie and no browser: it draws English. */
function getServerSnapshot(): Lang {
  return "en";
}

export interface I18nValue {
  /** The language on screen. */
  lang: Lang;
  /** Change the language of the whole interface and remember it. */
  setLang: (lang: Lang) => void;
  /** The string for a key, with its `{name}` placeholders filled in. */
  t: <K extends TranslationKey>(
    key: K,
    values?: TranslateValues<K>,
  ) => string;
}

const I18nContext = createContext<I18nValue | null>(null);

export function I18nProvider({ children }: { children: React.ReactNode }) {
  const lang = useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);

  const setLang = useCallback((next: Lang) => {
    resolved = next;
    setCurrentLang(next);
    writeLangCookie(next);
    for (const listener of listeners) listener();
  }, []);

  const t = useCallback(
    function translateKey<K extends TranslationKey>(
      key: K,
      values?: TranslateValues<K>,
    ): string {
      return translate(lang, key, values);
    },
    [lang],
  );

  // The document follows the language too, for the screen reader and the
  // browser's own hyphenation and date rules.
  useEffect(() => {
    document.documentElement.lang = lang;
  }, [lang]);

  const value = useMemo(
    () => ({ lang, setLang, t }),
    [lang, setLang, t],
  );

  return (
    <I18nContext.Provider value={value}>{children}</I18nContext.Provider>
  );
}

/** The language, the setter and the translator. */
export function useI18n(): I18nValue {
  const ctx = useContext(I18nContext);
  if (!ctx) throw new Error("useI18n must be used inside I18nProvider");
  return ctx;
}

/** Just the translator, for the many components that need nothing else. */
export function useT(): I18nValue["t"] {
  return useI18n().t;
}
