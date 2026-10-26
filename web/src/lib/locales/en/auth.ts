/** The auth strings — the English source of the shape. */
export const auth = {
} as const;

/** The auth strings with their literal types. */
export type AuthDictionary = { [K in keyof typeof auth]: string };
