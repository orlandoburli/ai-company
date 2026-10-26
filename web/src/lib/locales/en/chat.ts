/** The chat strings — the English source of the shape. */
export const chat = {
  /* --- the page ---------------------------------------------------------------- */
  "chat.title": "Chat",
  "chat.breadcrumb.item": "item #{number}",
  "chat.breadcrumb.unavailable": "item (unavailable)",
  "chat.loadFailed": "That chat thread could not load.",
  /* --- the panel ---------------------------------------------------------------- */
  "chat.hint.write": "Mention an agent with @name to route work.",
  "chat.hint.readOnly": "Read-only view — guests can read the chat but not write.",
  "chat.fullPage.aria": "Open chat as a full page",
  "chat.fullPage": "Full page",
  "chat.runAgent": "Run agent",
  "chat.conversation.loadFailed": "The conversation could not load.",
  "chat.history.loadFailed": "Run history could not load.",
  "chat.loadFailedShort": "Load failed.",
  "chat.empty.title": "No messages yet",
  "chat.empty.body": "Write the first message and mention an agent with @name — every enabled, mentioned agent gets its own run. The reply streams back here.",
  "chat.readOnlyHint": "Members can post messages and run agents. Resize the panel or open the full page to read comfortably.",
  "chat.cancelRequested": "Cancel requested for the run.",
  "chat.runQueued": "Run queued.",
  /* --- the roles ---------------------------------------------------------------- */
  "chat.role.system": "system",
  "chat.role.agent": "agent",
  "chat.role.member": "member",
  "chat.role.someone": "someone",
  "chat.role.you": "(you)",
  /* --- the runs ----------------------------------------------------------------- */
  "chat.jobState.queued": "queued",
  "chat.jobState.running": "running",
  "chat.jobState.succeeded": "succeeded",
  "chat.jobState.failed": "failed",
  "chat.jobState.cancelled": "cancelled",
  "chat.cancel.aria": "Cancel the run by @{agent}",
  "chat.cancel": "Cancel",
  "chat.cancelling": "Cancelling…",
  "chat.elapsed.clock": "{minutes}:{seconds} elapsed",
  "chat.elapsed.hours": "{hours}h{minutes}m elapsed",
  /* --- the composer --------------------------------------------------------------- */
  "chat.mention.aria": "Mention an agent",
  "chat.mention.title": "Mention an agent",
  "chat.mention.hint": "↑↓ to move · Enter to pick · Esc to dismiss",
  "chat.mention.noAgents": "No enabled agents yet. Add and enable one in Settings → Agents.",
  "chat.noRunner": "no runner",
  "chat.placeholder": "Write to the thread… type @ to mention an agent",
  "chat.messageAria": "Message",
  "chat.composer.hint": "Enter to send · Shift+Enter for a new line · @name routes the message to an agent",
  "chat.sending": "Sending…",
  "chat.send": "Send",
  /* --- the run agent dialog --------------------------------------------------------- */
  "chat.runDialog.title": "Run an agent",
  "chat.runDialog.description": "The agent gets the thread context and its soul file. Optional instructions are recorded as your message on the thread.",
  "chat.runDialog.noAgents": "No enabled agents yet. Add and enable one in Settings → Agents first.",
  "chat.runDialog.close": "Close",
  "chat.runDialog.agent": "Agent",
  "chat.runDialog.roleHint": "Role {role} · runs at most {count} at a time.",
  "chat.runDialog.instructions": "Instructions (optional)",
  "chat.runDialog.instructionsPlaceholder": "What should the agent focus on this run?",
  "chat.runDialog.queuing": "Queuing…",
  /* --- the errors -------------------------------------------------------------------- */
  "chat.err.unknownAgent": "That agent does not exist.",
  "chat.err.agentDisabled": "That agent is disabled.",
  "chat.err.queueFull": "The queue is full — try again in a moment.",
} as const;

/** The chat strings with their literal types. */
export type ChatDictionary = { [K in keyof typeof chat]: string };
