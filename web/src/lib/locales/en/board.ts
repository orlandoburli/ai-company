/** The board strings — the English source of the shape. */
export const board = {
  /* --- the board ------------------------------------------------------------- */
  "board.title": "Board",
  "board.allProjects": "All projects",
  "board.labelSuffix": "label “{label}”",
  "board.newItem": "New item",
  "board.labelUnused": "{label} (no longer in use)",
  "board.labelsFailed": "Labels could not load.",
  "board.selected": "{count} selected|{count} selected",
  "board.clear": "Clear",
  "board.moveTo": "Move to:",
  "board.empty.title": "No cards to show",
  "board.empty.body": "Create a work item, or check the filters above — an active filter can hide everything.",
  "board.empty.readOnly": "Nothing matches the filters. Ask someone with write access to add work.",
  "board.moveFailed": "Could not move the card.",
  "board.column": "{status} column",
  "board.addToColumn": "Add item to {status}",
  "board.dropHere": "Drop here to move to {status}.",
  "board.pin": "Pin to rank 1",
  "board.unpin": "Unpin",
  "board.pause": "Pause",
  "board.unpause": "Unpause",
  "board.select": "Select",
  "board.deselect": "Deselect",
  "board.assigned": "Assigned",
  "board.unassigned": "Unassigned",
} as const;

/** The board strings with their literal types. */
export type BoardDictionary = { [K in keyof typeof board]: string };
