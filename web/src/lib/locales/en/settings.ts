/** The settings strings — the English source of the shape. */
export const settings = {
} as const;

/** The settings strings with their literal types. */
export type SettingsDictionary = { [K in keyof typeof settings]: string };
