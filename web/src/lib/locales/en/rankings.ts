/** The rankings strings — the English source of the shape. */
export const rankings = {
} as const;

/** The rankings strings with their literal types. */
export type RankingsDictionary = { [K in keyof typeof rankings]: string };
