/** The workflows strings — the English source of the shape. */
export const workflows = {
} as const;

/** The workflows strings with their literal types. */
export type WorkflowsDictionary = { [K in keyof typeof workflows]: string };
