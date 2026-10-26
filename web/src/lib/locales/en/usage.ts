/** The usage strings — the English source of the shape. */
export const usage = {
} as const;

/** The usage strings with their literal types. */
export type UsageDictionary = { [K in keyof typeof usage]: string };
