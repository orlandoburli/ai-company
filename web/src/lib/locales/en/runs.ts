/** The runs strings — the English source of the shape. */
export const runs = {
} as const;

/** The runs strings with their literal types. */
export type RunsDictionary = { [K in keyof typeof runs]: string };
