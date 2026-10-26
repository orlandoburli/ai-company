/** The item strings — the English source of the shape. */
export const item = {
} as const;

/** The item strings with their literal types. */
export type ItemDictionary = { [K in keyof typeof item]: string };
