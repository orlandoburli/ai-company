/** The monitor strings — the English source of the shape. */
export const monitor = {
} as const;

/** The monitor strings with their literal types. */
export type MonitorDictionary = { [K in keyof typeof monitor]: string };
