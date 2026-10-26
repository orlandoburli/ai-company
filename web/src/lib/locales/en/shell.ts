/** The shell strings — the English source of the shape. */
export const shell = {
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
  /* --- the top bar ----------------------------------------------------------- */
  "topbar.toggleTree": "Toggle product tree",
  "topbar.breadcrumb": "Breadcrumb",
  /* --- the tenant switcher --------------------------------------------------- */
  "tenant.current": "Current company, open to switch",
  "tenant.switch": "Switch company",
  "tenant.currentBadge": "current",
  /* --- the account menu ------------------------------------------------------ */
  "account.menu": "Account menu",
  "account.settings": "Settings",
  "account.signOut": "Sign out",
} as const;

/** The shell strings with their literal types. */
export type ShellDictionary = { [K in keyof typeof shell]: string };
