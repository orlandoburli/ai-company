// Command seed populates a development database with a tenant, its owner and a
// realistic backlog across the three example products.
//
// It is a development tool: it talks to the same runtime connection as the
// server and goes through the ordinary stores, so everything it writes obeys the
// same RLS policies, numbering rules and activity logging. Nothing here is a
// back door around the application.
//
// Re-running is safe. If the tenant slug or the owner email already exists the
// command reports that the database is already seeded and exits 0, which is what
// lets `make dev` run it unconditionally.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/auth"
	"github.com/orlandoburli/ai-company/internal/runner"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "seed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	dsn := flag.String("dsn", os.Getenv("DATABASE_URL"), "Postgres connection URL (defaults to $DATABASE_URL)")
	email := flag.String("email", "owner@acme.dev", "owner email")
	password := flag.String("password", "correct-horse-battery-staple", "owner password (min 12 characters)")
	name := flag.String("name", "Orlando Burli", "owner name")
	tenantName := flag.String("tenant", "Acme Studio", "tenant display name")
	slug := flag.String("slug", "acme", "tenant slug (unique; used to detect an existing seed)")
	timeout := flag.Duration("timeout", 60*time.Second, "overall timeout")
	withItems := flag.Bool("items", true, "seed the demonstration backlog (the items, their dependencies and their labels)")
	flag.Parse()

	if strings.TrimSpace(*dsn) == "" {
		return errors.New("missing DSN: set DATABASE_URL or pass -dsn")
	}
	if strings.TrimSpace(*slug) == "" || strings.TrimSpace(*email) == "" {
		return errors.New("-slug and -email are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	db, err := sql.Open("pgx", *dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	// The seed is a single sequential script; one connection keeps the activity
	// log readable and avoids pool churn.
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	hash, err := auth.HashPassword(*password)
	if err != nil {
		return err
	}

	tenancyStore := tenancy.NewStore(db)
	identity, _, err := tenancyStore.RegisterTenant(ctx, *email, hash, *name, *tenantName, *slug)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			switch pgErr.ConstraintName {
			case "tenants_slug_key":
				slog.Info("tenant already seeded, nothing to do", "slug", *slug)
				// Still make sure the M2 agent registry exists: an existing dev
				// tenant from before agents were introduced should gain them
				// without duplicating anything else.
				if err := ensureSeedAgents(ctx, db, tenancyStore, *email, *slug); err != nil {
					return fmt.Errorf("ensure agents: %w", err)
				}
				if err := ensureSeedWorkflowTemplates(ctx, db, tenancyStore, *email, *slug, slog.Default()); err != nil {
					return fmt.Errorf("ensure workflow templates: %w", err)
				}
				fmt.Printf("Already seeded: tenant %q exists. Nothing new was created; the sample agents and templates are ensured.\n", *slug)
				return nil
			case "users_email_key":
				return fmt.Errorf("email %q already belongs to another tenant; pass -email with a different address", *email)
			}
		}
		return err
	}

	sc := tenancy.Scope{TenantID: identity.TenantID, UserID: identity.UserID, Role: identity.Role}
	store := work.NewStore(db)

	seeder := &seeder{store: store, sc: sc, ownerID: identity.UserID, withItems: *withItems}
	started := time.Now()
	if err := seeder.seedAll(ctx); err != nil {
		return err
	}
	if err := ensureSeedAgents(ctx, db, tenancyStore, *email, *slug); err != nil {
		return fmt.Errorf("seed agents: %w", err)
	}
	if err := ensureSeedWorkflowTemplates(ctx, db, tenancyStore, *email, *slug, slog.Default()); err != nil {
		return fmt.Errorf("seed workflow templates: %w", err)
	}

	fmt.Printf(`Seeded tenant %q (%s) in %s.

  Sign in:  %s
  Password: %s

  Products: %d   Projects: %d   Items: %d   Dependencies: %d

The tenant is the only one this command creates; re-running is a no-op.
`, *tenantName, *slug, time.Since(started).Round(time.Millisecond),
		strings.ToLower(*email), *password,
		seeder.products, seeder.projects, seeder.items, seeder.deps)
	return nil
}

// --- seed data -------------------------------------------------------------

type seeder struct {
	store   *work.Store
	sc      tenancy.Scope
	ownerID string
	// withItems seeds the demonstration backlog. With it off, the tenant gets
	// its products, its projects, its agents and its templates — and an empty
	// backlog, for the people who want to write their own tasks.
	withItems bool
	products  int
	projects  int
	items     int
	deps      int
}

type itemSeed struct {
	key      string
	title    string
	kind     string
	status   string
	prio     string // "", p0, p1, p2
	body     string
	labels   []string
	dueDays  int
	blocked  []string // keys of other items in the same product
	pinned   bool
	paused   string // non-empty pauses the item with this reason
	assigned bool
	children []itemSeed
}

type projectSeed struct {
	name  string
	key   string
	items []itemSeed
}

type productSeed struct {
	name     string
	slug     string
	repo     string
	jira     string
	projects []projectSeed
}

func (s *seeder) seedAll(ctx context.Context) error {
	for _, p := range products() {
		if err := s.seedProduct(ctx, p); err != nil {
			return fmt.Errorf("%s: %w", p.name, err)
		}
	}
	return nil
}

func (s *seeder) seedProduct(ctx context.Context, p productSeed) error {
	prod, err := s.store.CreateProduct(ctx, s.sc, p.name, p.slug, p.repo, p.jira)
	if err != nil {
		return fmt.Errorf("create product: %w", err)
	}
	s.products++

	// Keys are scoped to the product so two products may reuse a leaf name.
	ids := map[string]string{}
	type pendingDep struct {
		item    string
		blocked []string
	}
	var pending []pendingDep

	for _, proj := range p.projects {
		project, err := s.store.CreateProject(ctx, s.sc, prod.ID, nil, proj.name, proj.key)
		if err != nil {
			return fmt.Errorf("create project %q: %w", proj.name, err)
		}
		s.projects++

		if !s.withItems {
			// The structure without the demonstration backlog: the products,
			// the projects and the agents are there, the tasks are yours.
			continue
		}

		var create func(parentID *string, seeds []itemSeed) error
		create = func(parentID *string, seeds []itemSeed) error {
			for _, it := range seeds {
				created, err := s.createItem(ctx, project.ID, parentID, it)
				if err != nil {
					return err
				}
				ids[it.key] = created.ID
				if len(it.blocked) > 0 {
					pending = append(pending, pendingDep{item: it.key, blocked: it.blocked})
				}
				if len(it.children) > 0 {
					childParent := created.ID
					if err := create(&childParent, it.children); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err := create(nil, proj.items); err != nil {
			return fmt.Errorf("create items in %q: %w", proj.name, err)
		}
	}

	for _, d := range pending {
		for _, blocker := range d.blocked {
			from, ok := ids[d.item]
			if !ok {
				return fmt.Errorf("dependency references unknown item %q", d.item)
			}
			to, ok := ids[blocker]
			if !ok {
				return fmt.Errorf("dependency references unknown item %q", blocker)
			}
			if _, err := s.store.CreateDependency(ctx, s.sc, from, to, s.ownerID); err != nil {
				return fmt.Errorf("create dependency %s -> %s: %w", d.item, blocker, err)
			}
			s.deps++
		}
	}
	return nil
}

func (s *seeder) createItem(ctx context.Context, projectID string, parentID *string, it itemSeed) (work.Item, error) {
	in := work.NewItem{
		ProjectID:     projectID,
		ParentID:      parentID,
		Kind:          it.kind,
		Title:         it.title,
		DescriptionMD: it.body,
		Status:        it.status,
		Labels:        it.labels,
		ActorUserID:   s.ownerID,
	}
	if it.prio != "" {
		prio := it.prio
		in.Priority = &prio
	}
	if it.dueDays != 0 {
		due := time.Now().AddDate(0, 0, it.dueDays)
		in.DueDate = &due
	}
	if it.assigned {
		kind, user := "human", s.ownerID
		in.AssigneeKind, in.AssigneeUserID = &kind, &user
	}

	created, err := s.store.CreateItem(ctx, s.sc, in)
	if err != nil {
		return work.Item{}, fmt.Errorf("create item %q: %w", it.title, err)
	}
	s.items++

	if it.paused != "" {
		if _, err := s.store.PauseItem(ctx, s.sc, created.ID, it.paused, s.ownerID); err != nil {
			return work.Item{}, fmt.Errorf("pause %q: %w", it.title, err)
		}
	}
	if it.pinned {
		if _, err := s.store.PinItem(ctx, s.sc, created.ID, s.ownerID); err != nil {
			return work.Item{}, fmt.Errorf("pin %q: %w", it.title, err)
		}
	}
	return created, nil
}

// products is the development dataset: three products at different stages, with
// epics, mixed statuses, real-looking dependencies, one paused item and one pin.
func products() []productSeed {
	return []productSeed{
		{
			name: "ERP", slug: "erp",
			// A real repository of the tenant's: the GitHub wave opens pull
			// requests against it. The SSH URL keeps the token out of the
			// process arguments.
			repo: "git@github.com:orlandoburli/project-erp.git", jira: "ERP",
			projects: []projectSeed{
				{
					name: "Plataforma", key: "ERP",
					items: []itemSeed{
						{
							key: "fechamento", title: "Fechamento do período fiscal", kind: "epic", status: "in_progress",
							prio: "p1", labels: []string{"billing"}, assigned: true,
							body: "Consolidar custo, receita e produtividade por centro de custo ao fim do período.",
							children: []itemSeed{
								{
									key: "notas", title: "Importar notas fiscais (XML)", kind: "task",
									status: "done", prio: "p1", labels: []string{"import"},
									body: "Leitura do XML da NF-e e vínculo com o título correspondente.",
								},
								{
									key: "custo", title: "Consolidar custo por centro de custo", kind: "story",
									status: "in_progress", prio: "p1", labels: []string{"reporting"},
									dueDays: 5, assigned: true,
									body: "Somar insumos, mão de obra e encargos por centro de custo.",
								},
								{
									key: "pdf", title: "Exportar fechamento em PDF", kind: "task",
									status: "backlog", prio: "p2", labels: []string{"reporting"},
									blocked: []string{"custo"},
									body:    "Relatório assinável para o contador.",
								},
							},
						},
						{
							key: "estoque", title: "Saldo de estoque fica negativo após estorno", kind: "bug",
							status: "todo", prio: "p0", labels: []string{"bugs", "inventory"},
							dueDays: 2, assigned: true,
							body: "Reproduzido com duas saídas e um estorno no mesmo dia. O saldo deveria voltar ao valor anterior.",
						},
						{
							key: "ratelimit", title: "Rate limit no login por IP", kind: "task",
							status: "backlog", prio: "p2", labels: []string{"security"},
							body: "Hoje o login aceita tentativas ilimitadas.",
						},
						{
							key: "conciliacao", title: "Conciliação bancária automática", kind: "spike",
							status: "waiting_human", prio: "p2", labels: []string{"banking"},
							paused: "Precisa de decisão sobre a fonte dos extratos",
							body:   "Investigar a importação OFX e o casamento de PIX.",
						},
					},
				},
				{
					name: "Integrações", key: "ERPX",
					items: []itemSeed{
						{
							key: "tef", title: "Sincronizar pagamentos com o TEF", kind: "task",
							status: "waiting_human", prio: "p1", labels: []string{"integration"},
							body: "Bloqueado no credenciamento do banco.",
						},
						{
							key: "bancocentral", title: "Avaliar API de cotações do Banco Central", kind: "spike",
							status: "in_review", prio: "p2", labels: []string{"integration", "research"},
							body: "Comparar endpoints e limites de uso.",
						},
					},
				},
			},
		},
		{
			name: "Apiary", slug: "apiary",
			repo: "git@github.com:orlandoburli/apiary.git", jira: "API",
			projects: []projectSeed{
				{
					name: "Orquestração", key: "API",
					items: []itemSeed{
						{
							key: "fila", title: "Fila de agentes por prioridade", kind: "epic",
							status: "in_progress", prio: "p0", labels: []string{"core"}, assigned: true,
							body: "Executar o próximo item pronto, com revisão humana nos pontos de decisão.",
							children: []itemSeed{
								{
									key: "runners", title: "Configurar runners por agente", kind: "story",
									status: "done", prio: "p0",
									body: "CRUD de runners, prioridade e credencial.",
								},
								{
									key: "handoff", title: "Passar o trabalho entre agentes", kind: "task",
									status: "todo", prio: "p1", labels: []string{"core"},
									blocked: []string{"runners"},
									body:    "Encaminhar com o contexto e a auditoria intactos.",
								},
								{
									key: "stale", title: "Sinalizar etapa parada há 14 dias", kind: "task",
									status: "backlog", prio: "p2", labels: []string{"automation"},
									body: "Notificação diária com etapas sem movimento.",
								},
							},
						},
						{
							key: "dedupe", title: "Duplicidade de jobs na reexecução", kind: "bug",
							status: "in_progress", prio: "p0", labels: []string{"queue", "bugs"},
							dueDays: 1, assigned: true,
							body: "A reexecução cria um segundo job em vez de retomar o existente.",
						},
						{
							key: "custo", title: "Relatório de custo por modelo", kind: "story",
							status: "backlog", prio: "p1", labels: []string{"reporting"},
							blocked: []string{"fila"},
							body:    "Tokens e custo por runner e por agente.",
						},
						{
							key: "pin", title: "Corrigir o cálculo de custo no fechamento", kind: "bug",
							status: "todo", prio: "p1", labels: []string{"cost", "bugs"},
							pinned: true, assigned: true,
							body: "O custo usa o valor bruto em vez do líquido. Pinado porque o fechamento é amanhã.",
						},
					},
				},
			},
		},
		{
			name: "ProntaLoja", slug: "prontaloja",
			// The real repository of this product: the dogfood runs against
			// the actual project, not a demo copy.
			repo: "git@github.com:orlandoburli/prontaloja.git", jira: "PL",
			projects: []projectSeed{
				{
					name: "Storefront", key: "PL",
					items: []itemSeed{
						{
							key: "checkout", title: "Checkout em uma etapa", kind: "epic",
							status: "todo", prio: "p0", labels: []string{"conversion"},
							body: "Reduzir abandono unificando carrinho, entrega e pagamento.",
							children: []itemSeed{
								{
									key: "cart", title: "Carrinho lateral com edição de quantidade", kind: "story",
									status: "todo", prio: "p1", labels: []string{"frontend"},
									body: "Drawer acessível, sem recarregar a página.",
								},
								{
									key: "pix", title: "Pagamento via Pix", kind: "task",
									status: "backlog", prio: "p0", labels: []string{"payments"},
									blocked: []string{"cart"},
									body:    "QR code com expiração e webhook de confirmação.",
								},
							},
						},
						{
							key: "seo", title: "Sitemap por categoria", kind: "task",
							status: "done", prio: "p1", labels: []string{"seo"},
							body: "Sitemap dividido por categoria com limite de 50k URLs.",
						},
						{
							key: "slow", title: "Listagem demora 4s com 2k produtos", kind: "bug",
							status: "todo", prio: "p0", labels: []string{"performance", "bugs"},
							dueDays: 3, assigned: true,
							body: "Sem índice composto em (tenant_id, category_id, created_at).",
						},
						{
							key: "abandon", title: "Recuperação de carrinho abandonado", kind: "story",
							status: "backlog", prio: "p2", labels: []string{"automation"},
							body: "E-mail em 1h e 24h, com opt-out.",
						},
						{
							key: "old", title: "Remover integração com gateway descontinuado", kind: "task",
							status: "cancelled", prio: "p2", labels: []string{"cleanup"},
							body: "Contrato encerrado; nenhum tenant usa desde março.",
						},
					},
				},
			},
		},
	}
}

// ensureSeedAgents makes the two sample agents available on fresh and existing
// dev tenants alike. It never overwrites an agent a human has edited: names are
// the identity, and an existing row is left exactly as it is.
func ensureSeedAgents(ctx context.Context, db *sql.DB, ts *tenancy.Store, email, slug string) error {
	user, err := ts.FindUserByEmail(ctx, email)
	if err != nil {
		return fmt.Errorf("find seed user: %w", err)
	}
	memberships, err := ts.MembershipsForUser(ctx, user.ID)
	if err != nil {
		return fmt.Errorf("list memberships: %w", err)
	}
	var tenantID string
	for _, m := range memberships {
		if m.TenantSlug == slug {
			tenantID = m.TenantID
			break
		}
	}
	if tenantID == "" {
		return fmt.Errorf("user %s is not a member of tenant %q", email, slug)
	}

	sc := tenancy.Scope{TenantID: tenantID, UserID: user.ID, Role: tenancy.RoleOwner}
	store := agent.NewStore(db)

	devSoul := `You are **dev**, the implementation agent for this tenant.

You write and explain code, read the repository when a worktree is provided,
and keep changes small and reversible. You do not commit, push, open pull
requests or start workflows yourself: the server handles those after a human
checkpoint. When a request is ambiguous, ask one concrete question instead of
guessing.`
	pmSoul := `You are **pm**, the product agent for this tenant.

You keep the backlog honest: turn vague requests into scoped items, surface
dependencies and risks, and write crisp acceptance criteria. You do not invent
status. You are a thinking partner for the owner, not an approval authority.`

	agents := []agent.Agent{
		{
			Name:             "dev",
			Role:             agent.RoleDev,
			Kind:             agent.KindCLI,
			Config:           json.RawMessage(`{"bin":"claude","args":["-p","--output-format","stream-json","--verbose"],"output_format":"stream-json","timeout_sec":300}`),
			Capabilities:     json.RawMessage(`{"code":true,"shell":true,"streaming":true,"usage":true}`),
			ConcurrencyLimit: 2,
			Enabled:          true,
			SoulMD:           devSoul,
			SkillsPolicy:     json.RawMessage(`{"auto":true,"max_attach":3}`),
			Fallbacks:        json.RawMessage(`[]`),
			CreatedBy:        &user.ID,
		},
		{
			Name:             "reviewer",
			Role:             agent.RoleReviewer,
			Kind:             agent.KindCLI,
			Config:           json.RawMessage(`{"bin":"claude","args":["-p","--output-format","stream-json","--verbose"],"output_format":"stream-json","timeout_sec":300}`),
			Capabilities:     json.RawMessage(`{"code":false,"shell":false,"streaming":true,"usage":true}`),
			ConcurrencyLimit: 2,
			Enabled:          true,
			SoulMD: `You are **reviewer**, the review agent for this tenant.

You read the work and report what is wrong with it: incorrect behaviour, missing
cases, scope creep. You do not change code and you do not approve your own
findings. Say what you checked and what you could not check.`,
			SkillsPolicy: json.RawMessage(`{"auto":true,"max_attach":3}`),
			Fallbacks:    json.RawMessage(`[]`),
			CreatedBy:    &user.ID,
		},
		{
			Name:             "qa",
			Role:             agent.RoleQA,
			Kind:             agent.KindCLI,
			Config:           json.RawMessage(`{"bin":"claude","args":["-p","--output-format","stream-json","--verbose"],"output_format":"stream-json","timeout_sec":300}`),
			Capabilities:     json.RawMessage(`{"code":false,"shell":false,"streaming":true,"usage":true}`),
			ConcurrencyLimit: 2,
			Enabled:          true,
			SoulMD: `You are **qa**, the verification agent for this tenant.

You check whether the work does what it claims: run the checks that exist, read
the output, and report plainly whether they pass. You do not fix what you find;
you say what failed and how you know.`,
			SkillsPolicy: json.RawMessage(`{"auto":true,"max_attach":3}`),
			Fallbacks:    json.RawMessage(`[]`),
			CreatedBy:    &user.ID,
		},
		{
			Name:             "pm",
			Role:             agent.RolePM,
			Kind:             agent.KindAPI,
			Config:           json.RawMessage(`{"provider":"openai","model":"gpt-4o-mini","key_env":"OPENAI_API_KEY","max_tokens":1024}`),
			Capabilities:     json.RawMessage(`{"code":false,"shell":false,"streaming":false,"usage":true}`),
			ConcurrencyLimit: 2,
			Enabled:          true,
			SoulMD:           pmSoul,
			SkillsPolicy:     json.RawMessage(`{"auto":true,"max_attach":3}`),
			Fallbacks:        json.RawMessage(`[]`),
			CreatedBy:        &user.ID,
		},
	}

	runners := runner.NewStore(db)
	for _, a := range agents {
		existing, err := store.GetAgentByName(ctx, sc, a.Name)
		switch {
		case err == nil:
			// The agent exists: make sure it has a runner to run on.
			if err := ensureSeedRunner(ctx, runners, sc, existing); err != nil {
				return err
			}
			continue
		case !errors.Is(err, agent.ErrNotFound):
			return err
		}
		created, err := store.CreateAgent(ctx, sc, a)
		if err != nil {
			return fmt.Errorf("create agent %s: %w", a.Name, err)
		}
		if err := ensureSeedRunner(ctx, runners, sc, created); err != nil {
			return err
		}
	}
	return nil
}

// ensureSeedRunner gives an agent the runner it runs on: one runner carrying
// the agent's transport, bound to it as the first link of its chain. It is
// the same rule the schema's conversion uses, done through the stores so the
// seed runs with the ordinary runtime role.
func ensureSeedRunner(ctx context.Context, runners *runner.Store, sc tenancy.Scope, a agent.Agent) error {
	kind := string(a.Kind)
	if kind == "" {
		kind = string(runner.KindCLI)
	}
	config := a.Config
	if len(config) == 0 {
		config = json.RawMessage("{}")
	}
	r, found, err := runners.GetByName(ctx, sc, a.Name)
	if err != nil {
		return fmt.Errorf("find runner %s: %w", a.Name, err)
	}
	if !found {
		r, err = runners.Create(ctx, sc, runner.NewRunner{
			Name:   a.Name,
			Kind:   runner.Kind(kind),
			Config: config,
		})
		if err != nil {
			return fmt.Errorf("create runner %s: %w", a.Name, err)
		}
	}
	chain, err := runners.Chain(ctx, sc, a.ID)
	if err != nil {
		return err
	}
	for _, existing := range chain {
		if existing.ID == r.ID {
			return nil
		}
	}
	ids := []string{r.ID}
	for _, existing := range chain {
		ids = append(ids, existing.ID)
	}
	if err := runners.SetChain(ctx, sc, a.ID, ids); err != nil {
		return fmt.Errorf("bind runner %s to agent %s: %w", r.Name, a.Name, err)
	}
	return nil
}
