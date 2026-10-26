import type { Dictionary } from "./en";

/**
 * Brazilian Portuguese. Typed as `Dictionary`, so every key of the English
 * dictionary has to be here: a missing key is a type error, never a blank.
 *
 * Vocabulary the product owns stays as the product spells it — backlog,
 * runner, workflow — while everything else is translated: board is "quadro",
 * a checkpoint is a "ponto de decisão", a run is an "execução", a queue is a
 * "fila", an agent is an "agente", settings are "configurações".
 */
export const ptBR: Dictionary = {
  /* --- o seletor de idioma --------------------------------------------------- */
  "lang.switcher.label": "Idioma",
  "lang.switcher.title": "Mudar o idioma",
  "lang.en.name": "Inglês",
  "lang.en.short": "EN",
  "lang.ptBR.name": "Português (Brasil)",
  "lang.ptBR.short": "PT",

  /* --- a navegação principal ------------------------------------------------- */
  "nav.board": "Quadro",
  "nav.backlog": "Backlog",
  "nav.queue": "Fila",
  "nav.runs": "Execuções",
  "nav.workflows": "Workflows",
  "nav.checkpoints": "Pontos de decisão",
  "nav.usage": "Uso",
  "nav.settings": "Configurações",
  "nav.rankings": "Ranking",
  "nav.page": "Página",

  /* --- a barra lateral ------------------------------------------------------- */
  "nav.section.main": "Principal",
  "nav.section.apps": "Aplicativos",
  "nav.section.products": "Produtos",
  "nav.label.primary": "Navegação principal",
  "nav.label.tree": "Árvore de produtos",
  "tree.failed": "Falha ao carregar a árvore.",
  "tree.noProducts": "Nenhum produto ainda. Crie um em Configurações.",
  "tree.noProjects": "Nenhum projeto.",

  /* --- o caminho de navegação ------------------------------------------------ */
  "crumb.workspace": "Área de trabalho",
  "crumb.home": "Início",
  "crumb.tasks": "Tarefas",
  "crumb.taskDetails": "Detalhes da tarefa",
  "crumb.runMonitor": "Monitor de execução",
  "crumb.workflowEditor": "Editor de workflow",
  "crumb.chat": "Chat",

  /* --- as seções de configurações (caminho) ----------------------------------- */
  "settings.agents": "Agentes",
  "settings.runners": "Runners",
  "settings.notifications": "Notificações",
  "settings.skills": "Skills",
  "settings.language": "Idioma",
  "settings.languageHint": "O idioma da interface. Vale para todas as telas.",

  /* --- a barra superior ------------------------------------------------------ */
  "topbar.toggleTree": "Alternar a árvore de produtos",
  "topbar.breadcrumb": "Trilha de navegação",

  /* --- o seletor de empresa -------------------------------------------------- */
  "tenant.current": "Empresa atual, clique para trocar",
  "tenant.switch": "Trocar empresa",
  "tenant.currentBadge": "atual",

  /* --- o centro de notificações ----------------------------------------------- */
  "notifications.title": "Notificações",
  "notifications.unread": "{count} não lida|{count} não lidas",
  "notifications.unavailable": "As notificações ainda não estão disponíveis.",
  "notifications.caughtUp": "Tudo em dia.",
  "notifications.openInbox": "Abrir a caixa de entrada de pontos de decisão",
  "notifications.pendingCheckpoints": "{count} ponto de decisão pendente|{count} pontos de decisão pendentes",

  /* --- os tipos de notificação ----------------------------------------------- */
  "notificationKind.checkpoint_pending": "Ponto de decisão pendente",
  "notificationKind.run_failed": "Execução falhou",
  "notificationKind.run_completed": "Execução concluída",
  "notificationKind.sync_error": "Erro de sincronização",

  /* --- o menu da conta ------------------------------------------------------- */
  "account.menu": "Menu da conta",
  "account.settings": "Configurações",
  "account.signOut": "Sair",

  /* --- as notificações de desktop --------------------------------------------- */
  "push.title": "Notificações no desktop",
  "push.on": "Ativo · neste navegador",
  "push.off": "Desligado",
  "push.checking": "Verificando…",
  "push.unavailable": "Indisponível",
  "push.working": "Processando…",
  "push.browser": "Navegador: {state}",
  "push.permission": "Permissão: {state}",
  "push.server": "Servidor: {state}",
  "push.setup": "Configuração: {reason}",
  "push.guest": "Convidados não podem se inscrever em notificações push.",
  "push.supported": "compatível",
  "push.unsupported": "incompatível",
  "push.state.saved": "assinatura salva",
  "push.state.removed": "assinatura removida",
  "push.state.notSubscribed": "não inscrito",
  "push.state.notChecked": "não verificado",
  "push.state.notReached": "não acessado",

  /* --- por que o push não funciona ------------------------------------------- */
  "push.err.noSupport": "Este navegador não oferece suporte a notificações.",
  "push.err.noServiceWorker": "Este navegador não oferece suporte a service workers, necessários para o push.",
  "push.err.noWorker": "Nenhum service worker instalado ainda. O push precisa de um para receber notificações.",
  "push.err.notRegistered": "Há uma assinatura do navegador, mas ela não está registrada neste servidor.",
  "push.err.noMatch": "Não foi possível associar a assinatura do navegador ao servidor.",
  "push.err.denied": "A permissão do navegador foi negada. Permita as notificações nas configurações do navegador para continuar.",
  "push.err.notGranted": "A permissão do navegador não foi concedida.",
  "push.err.incomplete": "O navegador não forneceu uma assinatura de push completa.",
  "push.err.generic": "Algo deu errado.",

  /* --- os status compartilhados ---------------------------------------------- */
  "status.backlog": "Backlog",
  "status.todo": "A fazer",
  "status.in_progress": "Em progresso",
  "status.waiting_human": "Aguardando pessoa",
  "status.in_review": "Em revisão",
  "status.done": "Concluído",
  "status.cancelled": "Cancelado",

  /* --- o quadro --------------------------------------------------------------- */
  "board.title": "Quadro",
  "board.allProjects": "Todos os projetos",
  "board.labelSuffix": "etiqueta “{label}”",
  "board.newItem": "Novo item",
  "board.labelUnused": "{label} (sem uso)",
  "board.labelsFailed": "Não foi possível carregar as etiquetas.",
  "board.selected": "{count} selecionado|{count} selecionados",
  "board.clear": "Limpar",
  "board.moveTo": "Mover para:",
  "board.empty.title": "Nenhum card para mostrar",
  "board.empty.body": "Crie um item de trabalho ou revise os filtros acima — um filtro ativo pode esconder tudo.",
  "board.empty.readOnly": "Nada corresponde aos filtros. Peça a alguém com acesso de escrita para adicionar trabalho.",
  "board.moveFailed": "Não foi possível mover o card.",
  "board.column": "Coluna {status}",
  "board.addToColumn": "Adicionar item em {status}",
  "board.dropHere": "Solte aqui para mover para {status}.",
  "board.pin": "Fixar no topo (posição 1)",
  "board.unpin": "Desafixar",
  "board.pause": "Pausar",
  "board.unpause": "Retomar",
  "board.select": "Selecionar",
  "board.deselect": "Desmarcar",
  "board.assigned": "Atribuído",
  "board.unassigned": "Sem responsável",

  /* --- o estado de um item --------------------------------------------------- */
  "item.pinned": "Fixado no topo (posição 1)",
  "item.paused": "Pausado",
  "item.pausedBy": "Pausado por {name}",
  "item.blocked": "Tem um bloqueio em aberto",
  "item.epic": "Épico: tem subitens em aberto",

  /* --- o diálogo de novo item ------------------------------------------------ */
  "newItem.title": "Novo item de trabalho",
  "newItem.description": "Os itens ficam em um projeto e podem ser aninhados sob outro item depois, na página dele.",
  "newItem.noProject": "Crie um projeto primeiro — esta empresa ainda não tem nenhum.",
  "newItem.kindError": "Não foi possível carregar os tipos de projeto; qualquer valor é aceito.",
  "newItem.kindLoading": "Carregando tipos de projeto…",
  "newItem.kindHint": "Escolha um tipo de projeto ou digite o seu.",
  "newItem.p0": "P0 — mais alta",
  "newItem.labelsError": "Não foi possível carregar as etiquetas; você ainda pode adicionar qualquer uma.",
  "newItem.labelsLoading": "Carregando etiquetas…",
  "newItem.labelsHint": "Escolha uma etiqueta em uso ou adicione uma nova.",
  "newItem.creating": "Criando…",
  "newItem.create": "Criar item",

  /* --- os controles compartilhados -------------------------------------------- */
  "field.kind": "Tipo",
  "field.projectKinds": "Tipos de projeto",
  "field.useValue": "Usar “{value}”",
  "field.noKinds": "Nenhum tipo de projeto ainda.",
  "field.addLabel": "Adicionar etiqueta",
  "field.addAnotherLabel": "Adicionar outra etiqueta…",
  "field.typeLabel": "Digite uma etiqueta…",
  "field.availableLabels": "Etiquetas disponíveis",
  "field.addValue": "Adicionar “{value}”",
  "field.removeLabel": "Remover a etiqueta {label}",
  "field.status": "Status",
  "field.priority": "Prioridade",
  "field.noPriority": "Sem prioridade",
  "field.product": "Produto",
  "field.project": "Projeto",
  "field.label": "Etiqueta",
  "field.labels": "Etiquetas",
  "field.assignee": "Responsável",
  "field.all": "Todos",
  "field.title": "Título",
  "field.cancel": "Cancelar",
  "field.confirm": "Confirmar",
  "error.title": "Algo deu errado.",
  "error.retry": "Tentar novamente",
  "dialog.close": "Fechar o diálogo",
  "copy.copy": "Copiar",
  "copy.copied": "Copiado",
};
