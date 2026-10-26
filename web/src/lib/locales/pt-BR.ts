/**
 * The Brazilian Portuguese dictionary. It is typed as the English one, so a key
 * that is missing or misspelled here fails the build.
 */

import { shell } from "./pt-BR/shell";
import { board } from "./pt-BR/board";
import { shared } from "./pt-BR/shared";
import { backlog } from "./pt-BR/backlog";
import { queue } from "./pt-BR/queue";
import { runs } from "./pt-BR/runs";
import { workflows } from "./pt-BR/workflows";
import { rankings } from "./pt-BR/rankings";
import { usage } from "./pt-BR/usage";
import { monitor } from "./pt-BR/monitor";
import { item } from "./pt-BR/item";
import { checkpoints } from "./pt-BR/checkpoints";
import { chat } from "./pt-BR/chat";
import { editor } from "./pt-BR/editor";
import { settings } from "./pt-BR/settings";
import { auth } from "./pt-BR/auth";
import type { Dictionary } from "./en";

export const ptBR: Dictionary = {
  ...shell,
  ...board,
  ...shared,
  ...backlog,
  ...queue,
  ...runs,
  ...workflows,
  ...rankings,
  ...usage,
  ...monitor,
  ...item,
  ...checkpoints,
  ...chat,
  ...editor,
  ...settings,
  ...auth,
};
