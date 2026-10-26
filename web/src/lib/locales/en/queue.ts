/** The queue strings — the English source of the shape. */
export const queue = {
} as const;

/** The queue strings with their literal types. */
export type QueueDictionary = { [K in keyof typeof queue]: string };
