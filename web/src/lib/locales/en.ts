/**
 * The English dictionary. It is the shape of every translation: its keys are
 * the translation keys of the product, its values are the templates.
 *
 * Every other locale is typed as `Dictionary`, so a key that exists here and
 * is missing there is a type error at build time — never a blank at runtime.
 * Placeholders are written `{name}` and are typed per key, so `t(key, values)`
 * only accepts the names the template actually uses.
 */

export const en = {
  /* --- the language switcher ------------------------------------------------ */
  "lang.switcher.label": "Language",
  "lang.switcher.title": "Change language",
  "lang.en.name": "English",
  "lang.en.short": "EN",
  "lang.ptBR.name": "Portuguese (Brazil)",
  "lang.ptBR.short": "PT",

  /* --- primary navigation ---------------------------------------------------- */
  "nav.board": "Board",
  "nav.backlog": "Backlog",
  "nav.queue": "Queue",
  "nav.runs": "Runs",
  "nav.workflows": "Workflows",
  "nav.checkpoints": "Checkpoints",
  "nav.usage": "Usage",
  "nav.settings": "Settings",
  "nav.rankings": "Rankings",
  "nav.page": "Page",

  /* --- the sidebar ----------------------------------------------------------- */
  "nav.section.main": "Main",
  "nav.section.apps": "Apps",
  "nav.section.products": "Products",
  "nav.label.primary": "Primary navigation",
  "nav.label.tree": "Product tree",
  "tree.failed": "Failed to load the tree.",
  "tree.noProducts": "No products yet. Create one in Settings.",
  "tree.noProjects": "No projects.",

  /* --- the breadcrumb -------------------------------------------------------- */
  "crumb.workspace": "Workspace",
  "crumb.home": "Home",
  "crumb.tasks": "Tasks",
  "crumb.taskDetails": "Task details",
  "crumb.runMonitor": "Run monitor",
  "crumb.workflowEditor": "Workflow editor",
  "crumb.chat": "Chat",

  /* --- the settings sections (breadcrumb) ------------------------------------- */
  "settings.agents": "Agents",
  "settings.runners": "Runners",
  "settings.notifications": "Notifications",
  "settings.skills": "Skills",
  "settings.language": "Language",
  "settings.languageHint": "The language of the interface. It applies to every screen.",

  /* --- the top bar ----------------------------------------------------------- */
  "topbar.toggleTree": "Toggle product tree",
  "topbar.breadcrumb": "Breadcrumb",

  /* --- the tenant switcher --------------------------------------------------- */
  "tenant.current": "Current company, open to switch",
  "tenant.switch": "Switch company",
  "tenant.currentBadge": "current",

  /* --- the notification centre ----------------------------------------------- */
  "notifications.title": "Notifications",
  "notifications.unread": "{count} unread|{count} unread",
  "notifications.unavailable": "Notifications are not available yet.",
  "notifications.caughtUp": "All caught up.",
  "notifications.openInbox": "Open checkpoint inbox",
  "notifications.pendingCheckpoints": "{count} pending checkpoint|{count} pending checkpoints",

  /* --- the kinds of notification --------------------------------------------- */
  "notificationKind.checkpoint_pending": "Checkpoint pending",
  "notificationKind.run_failed": "Run failed",
  "notificationKind.run_completed": "Run completed",
  "notificationKind.sync_error": "Sync error",

  /* --- the account menu ------------------------------------------------------ */
  "account.menu": "Account menu",
  "account.settings": "Settings",
  "account.signOut": "Sign out",

  /* --- desktop push ---------------------------------------------------------- */
  "push.title": "Desktop notifications",
  "push.on": "On · this browser",
  "push.off": "Off",
  "push.checking": "Checking…",
  "push.unavailable": "Unavailable",
  "push.working": "Working…",
  "push.browser": "Browser: {state}",
  "push.permission": "Permission: {state}",
  "push.server": "Server: {state}",
  "push.setup": "Setup: {reason}",
  "push.guest": "Guests can’t subscribe to push notifications.",
  "push.supported": "supported",
  "push.unsupported": "unsupported",
  "push.state.saved": "subscription saved",
  "push.state.removed": "subscription removed",
  "push.state.notSubscribed": "not subscribed",
  "push.state.notChecked": "not checked",
  "push.state.notReached": "not reached",

  /* --- why push is unavailable ----------------------------------------------- */
  "push.err.noSupport": "This browser does not support notifications.",
  "push.err.noServiceWorker": "This browser does not support service workers, which push needs.",
  "push.err.noWorker": "No service worker is installed yet. Push needs one to receive notifications.",
  "push.err.notRegistered": "A browser subscription exists but is not registered with this server.",
  "push.err.noMatch": "The browser subscription could not be matched to the server.",
  "push.err.denied": "Browser permission is denied. Allow notifications in browser settings to continue.",
  "push.err.notGranted": "Browser permission was not granted.",
  "push.err.incomplete": "The browser did not provide a complete push subscription.",
  "push.err.generic": "Something went wrong.",

  /* --- the shared statuses --------------------------------------------------- */
  "status.backlog": "Backlog",
  "status.todo": "To do",
  "status.in_progress": "In progress",
  "status.waiting_human": "Waiting on human",
  "status.in_review": "In review",
  "status.done": "Done",
  "status.cancelled": "Cancelled",

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

  /* --- the state of an item --------------------------------------------------- */
  "item.pinned": "Pinned to rank 1",
  "item.paused": "Paused",
  "item.pausedBy": "Paused by {name}",
  "item.blocked": "Has an open blocker",
  "item.epic": "Epic: has open sub-items",

  /* --- the new item dialog ---------------------------------------------------- */
  "newItem.title": "New work item",
  "newItem.description": "Items live in a project and can nest under another item later on its own page.",
  "newItem.noProject": "Create a project first — this company has none yet.",
  "newItem.kindError": "Project kinds could not load; any value is still allowed.",
  "newItem.kindLoading": "Loading project kinds…",
  "newItem.kindHint": "Choose a project kind or enter your own.",
  "newItem.p0": "P0 — highest",
  "newItem.labelsError": "Labels could not load; you can still add any label.",
  "newItem.labelsLoading": "Loading labels…",
  "newItem.labelsHint": "Choose a label in use or add a new one.",
  "newItem.creating": "Creating…",
  "newItem.create": "Create item",

  /* --- the shared controls --------------------------------------------------- */
  "field.kind": "Kind",
  "field.projectKinds": "Project kinds",
  "field.useValue": "Use “{value}”",
  "field.noKinds": "No project kinds yet.",
  "field.addLabel": "Add label",
  "field.addAnotherLabel": "Add another label…",
  "field.typeLabel": "Type a label…",
  "field.availableLabels": "Available labels",
  "field.addValue": "Add “{value}”",
  "field.removeLabel": "Remove label {label}",
  "field.status": "Status",
  "field.priority": "Priority",
  "field.noPriority": "No priority",
  "field.product": "Product",
  "field.project": "Project",
  "field.label": "Label",
  "field.labels": "Labels",
  "field.assignee": "Assignee",
  "field.all": "All",
  "field.title": "Title",
  "field.cancel": "Cancel",
  "field.confirm": "Confirm",
  "error.title": "Something went wrong.",
  "error.retry": "Try again",
  "dialog.close": "Close dialog",
  "copy.copy": "Copy",
  "copy.copied": "Copied",
} as const;

/** The English dictionary with its literal types — the source of the shape. */
export type EnglishDictionary = typeof en;

/**
 * The contract every locale must satisfy: every key of the English
 * dictionary, each holding a string. A missing key fails the build.
 */
export type Dictionary = { [K in keyof EnglishDictionary]: string };

/** Every key the interface may ask for. */
export type TranslationKey = keyof EnglishDictionary;

/** The names of the `{name}` placeholders inside a template. */
export type PlaceholderNames<S extends string> = S extends `${string}{${infer P}}${infer R}`
  ? P | PlaceholderNames<R>
  : never;

/**
 * The values a key accepts: the placeholders it declares, and nothing else.
 * A key without placeholders takes no values at all.
 */
export type TranslateValues<K extends TranslationKey> = PlaceholderNames<
  EnglishDictionary[K]
> extends never
  ? undefined
  : Partial<Record<PlaceholderNames<EnglishDictionary[K]>, string | number>>;
