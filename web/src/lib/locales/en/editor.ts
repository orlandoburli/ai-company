/** The editor strings — the English source of the shape. */
export const editor = {
} as const;

/** The editor strings with their literal types. */
export type EditorDictionary = { [K in keyof typeof editor]: string };
