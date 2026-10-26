/**
 * The English dictionary — the shape of every translation.
 *
 * It is composed from the per-screen parts under `./en`, so a screen can be
 * translated (and reviewed) on its own. Its keys are the translation keys of
 * the product and its values are the templates.
 *
 * Every other locale is typed as `Dictionary`, so a key that exists here and
 * is missing there is a type error at build time — never a blank at runtime.
 * Placeholders are written `{name}` and are typed per key, so `t(key, values)`
 * only accepts the names the template actually uses.
 */

import { shell } from "./en/shell";
import { board } from "./en/board";
import { shared } from "./en/shared";
import { backlog } from "./en/backlog";
import { queue } from "./en/queue";
import { runs } from "./en/runs";
import { workflows } from "./en/workflows";
import { rankings } from "./en/rankings";
import { usage } from "./en/usage";
import { monitor } from "./en/monitor";
import { item } from "./en/item";
import { checkpoints } from "./en/checkpoints";
import { chat } from "./en/chat";
import { editor } from "./en/editor";
import { settings } from "./en/settings";
import { auth } from "./en/auth";

export const en = {
  ...shell,
  ...board,
  ...shared,
  ...backlog,
  ...queue,
  ...runs,
  ...workflows,
  ...rankings,
  ...usage,
  ...monitor,
  ...item,
  ...checkpoints,
  ...chat,
  ...editor,
  ...settings,
  ...auth,
} as const;

/** The English dictionary with its literal types — the source of the shape. */
export type EnglishDictionary = typeof en;

/**
 * The contract every locale must satisfy: every key of the English
 * dictionary, each holding a string. A missing key fails the build.
 */
export type Dictionary = { [K in keyof EnglishDictionary]: string };

/** Every key the interface may ask for. */
export type TranslationKey = keyof EnglishDictionary;

/** The names of the `{name}` placeholders inside a template. */
export type PlaceholderNames<S extends string> = S extends `${string}{${infer P}}${infer R}`
  ? P | PlaceholderNames<R>
  : never;

/**
 * The values a key accepts: the placeholders it declares, and nothing else.
 * A key without placeholders takes no values at all.
 */
export type TranslateValues<K extends TranslationKey> = PlaceholderNames<
  EnglishDictionary[K]
> extends never
  ? undefined
  : Partial<Record<PlaceholderNames<EnglishDictionary[K]>, string | number>>;
