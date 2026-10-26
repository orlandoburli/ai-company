/** The backlog strings — the English source of the shape. */
export const backlog = {
} as const;

/** The backlog strings with their literal types. */
export type BacklogDictionary = { [K in keyof typeof backlog]: string };
