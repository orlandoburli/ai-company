/** The chat strings — the English source of the shape. */
export const chat = {
} as const;

/** The chat strings with their literal types. */
export type ChatDictionary = { [K in keyof typeof chat]: string };
