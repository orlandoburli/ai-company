import { en, type Dictionary, type TranslationKey, type TranslateValues } from "./en";
import { ptBR } from "./pt-BR";

export type { TranslationKey, TranslateValues } from "./en";

/** The languages the interface speaks. */
export type Lang = "en" | "pt-BR";

/** Every dictionary, by language. */
export const LOCALES: Record<Lang, Dictionary> = {
  en,
  "pt-BR": ptBR,
};

/** The languages offered by the switcher, with the keys of their names. */
export const LANGUAGES: {
  lang: Lang;
  shortKey: TranslationKey;
  nameKey: TranslationKey;
}[] = [
  { lang: "en", shortKey: "lang.en.short", nameKey: "lang.en.name" },
  { lang: "pt-BR", shortKey: "lang.ptBR.short", nameKey: "lang.ptBR.name" },
];

/** The cookie that remembers the choice between visits. */
export const LANG_COOKIE = "aic-lang";

/**
 * The language on screen right now. It is a module-level value because the
 * shared helpers (status labels, date formats) read it outside React; the
 * provider keeps it in step with the rendered language.
 */
let currentLang: Lang = "en";

export function getCurrentLang(): Lang {
  return currentLang;
}

export function setCurrentLang(lang: Lang): void {
  currentLang = lang;
}

/** Reads a cookie the browser is willing to show us. */
function readCookie(name: string): string | null {
  if (typeof document === "undefined") return null;
  for (const part of document.cookie.split(";")) {
    const [key, ...rest] = part.trim().split("=");
    if (key === name) return decodeURIComponent(rest.join("="));
  }
  return null;
}

/** Writes the cookie the choice is remembered in: a year, for the whole site. */
export function writeLangCookie(lang: Lang): void {
  if (typeof document === "undefined") return;
  document.cookie = `${LANG_COOKIE}=${encodeURIComponent(lang)}; path=/; max-age=31536000; samesite=lax`;
}

/**
 * The language of a first visit: the browser's own. pt-BR speaks Portuguese,
 * anything else speaks English.
 */
export function detectLang(): Lang {
  const stored = readCookie(LANG_COOKIE);
  if (stored === "en" || stored === "pt-BR") return stored;
  const browser = typeof navigator !== "undefined" ? navigator.language : "";
  return browser.toLowerCase() === "pt-br" ? "pt-BR" : "en";
}

/**
 * The string for a key in a language, with its `{name}` placeholders filled
 * from the values. An unknown key falls back to English and then to the key
 * itself, so the interface never shows a blank.
 *
 * A template may carry two forms separated by `|`: the first is used for a
 * count of one, the second for any other count. A template with one form is
 * used for every count, which is all English needs for most words.
 */
export function translate<K extends TranslationKey>(
  lang: Lang,
  key: K,
  values?: TranslateValues<K>,
): string {
  const dictionary = LOCALES[lang] as Record<string, string>;
  const fallback = en as unknown as Record<string, string>;
  const raw = dictionary[key] ?? fallback[key] ?? key;
  const bag = (values ?? {}) as Record<string, string | number>;
  const template = raw.includes("|")
    ? bag.count === 1
      ? raw.split("|")[0]
      : (raw.split("|")[1] ?? raw.split("|")[0])
    : raw;
  if (!values) return template;
  return template.replace(/\{(\w+)\}/g, (whole, name: string) => {
    const value = bag[name];
    return value === undefined ? whole : String(value);
  });
}
