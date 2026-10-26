/** The checkpoints strings — the English source of the shape. */
export const checkpoints = {
} as const;

/** The checkpoints strings with their literal types. */
export type CheckpointsDictionary = { [K in keyof typeof checkpoints]: string };
