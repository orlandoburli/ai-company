// Command server runs the AI Company control plane.
//
// It is the single process for the MVP: REST API, orchestrator and agent runtime
// share one binary and one database. Splitting them is a deployment choice, not
// an architectural one, and nothing here prevents it later.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/agent/adapters"
	"github.com/orlandoburli/ai-company/internal/api"
	"github.com/orlandoburli/ai-company/internal/command"
	"github.com/orlandoburli/ai-company/internal/dispatch"
	"github.com/orlandoburli/ai-company/internal/failure"
	"github.com/orlandoburli/ai-company/internal/ghsync"
	"github.com/orlandoburli/ai-company/internal/integration"
	"github.com/orlandoburli/ai-company/internal/memory"
	"github.com/orlandoburli/ai-company/internal/notification"
	"github.com/orlandoburli/ai-company/internal/push"
	"github.com/orlandoburli/ai-company/internal/ranking"
	"github.com/orlandoburli/ai-company/internal/runner"
	"github.com/orlandoburli/ai-company/internal/skill"
	"github.com/orlandoburli/ai-company/internal/store/migrations"
	"github.com/orlandoburli/ai-company/internal/store/retention"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
	"github.com/orlandoburli/ai-company/internal/workflow"
	"github.com/orlandoburli/ai-company/internal/workspace"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "server: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", envOr("AIC_ADDR", ":8080"), "listen address")
	dsn := flag.String("dsn", os.Getenv("DATABASE_URL"), "Postgres connection URL (defaults to $DATABASE_URL)")
	// Off by default: the runtime role must not be able to create tables. Run
	// cmd/migrate with an owner DSN as a deploy step instead. Leaving this on
	// would either need an over-privileged runtime role or fail at startup.
	applyMigrations := flag.Bool("migrate", false, "apply pending migrations before serving (requires a DSN that owns the schema)")
	secureCookies := flag.Bool("secure-cookies", envBool("AIC_SECURE_COOKIES", true),
		"mark the session cookie Secure (disable only for plain-http development)")
	flag.Parse()

	if *dsn == "" {
		return errors.New("set DATABASE_URL or pass -dsn")
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel()}))
	slog.SetDefault(log)

	db, err := sql.Open("pgx", *dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	// A small pool: this process is I/O bound on agent subprocesses, and Postgres
	// connections are a shared, finite resource.
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	if *applyMigrations {
		ran, err := migrations.Up(ctx, db)
		if err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		if len(ran) == 0 {
			log.Info("schema up to date")
		}
		for _, m := range ran {
			log.Info("migration applied", "migration", m)
		}
	}

	agentStore := agent.NewStore(db)
	agentHub := agent.NewHub()
	// The runner registry: the HOW half of the agent registry. The engine
	// resolves the runner of every node from it, the agent's chain is the
	// fallback order, and the worker reads it to resolve the credential a
	// job runs under.
	runnerStore := runner.NewStore(db)
	worker := agent.NewWorker(agentStore, agentHub,
		[]agent.Adapter{adapters.NewCLI(), adapters.NewAPI()},
		agent.WithWorkerLogger(log),
		// A runner's credential is a reference to a secret in the OS
		// keychain. The worker resolves it at the moment a job runs, so the
		// secret is never at rest in the database.
		agent.WithSecretResolver(integration.Credentials{}),
		agent.WithRunnerStore(runnerStore))
	worker.Start(ctx)

	// M3 stores: the workflow persistence, the memory tiers and the
	// notification centre. The engine (the scheduler that drives workflow
	// runs) is wired on top of them.
	workflowStore := workflow.NewStore(db)
	memoryStore := memory.NewStore(db)
	notifyStore := notification.NewStore(db)
	skillStore := skill.NewStore(db)

	// The engine is the scheduler: a stateless tick that recomputes the ready
	// set from the persisted state and dispatches the ready nodes. It shares
	// the process with the API and the agent worker, like everything else in
	// the MVP.
	// The platform connectors: the git operations run as the server (the agent
	// never pushes), and the GitHub client is built per repository from the
	// token the keychain holds — the token never reaches a URL, a log line or
	// the database.
	integrations := &workflow.Services{
		Git:    integration.NewGit(),
		Tokens: integration.Credentials{},
		GitHubFactory: func(token, owner, repo string) workflow.GitHubService {
			return integration.NewGitHub(http.DefaultClient, "https://api.github.com", token, owner, repo)
		},
	}

	workStore := work.NewStore(db)

	engine := workflow.NewEngine(workflow.EngineDeps{
		Store:         workflowStore,
		Agents:        agentStore,
		Memory:        memoryStore,
		Skills:        skillStore,
		Work:          workStore,
		Workspaces:    workspace.NewProvider(),
		Notifications: notifyStore,
		Runners:       runnerStore,
		Failures:      failure.NewStore(db),
		Integrations:  integrations,
		Push:          push.NewSender(push.NewStore(db), log),
		Commands:      command.NewRunner(db, log),
		Logger:        log,
	})
	engine.Start(ctx)

	// The dispatcher: the part that actually starts the work. It walks the
	// ranked queue, applies the eligibility gates before the order, and obeys
	// the global worker pool and the per-agent concurrency.
	tenantSource := dispatch.StoreTenants{DB: db}
	capacitySource := dispatch.StoreCapacity{DB: db, Agents: agentStore}
	dispatcher := dispatch.New(dispatch.Deps{
		Work:      workStore,
		Workflows: workflowStore,
		Engine:    engine,
		Agents:    agentStore,
		Rankings:  ranking.NewStore(db),
		Tenants:   tenantSource,
		Capacity:  capacitySource,
		Activity:  dispatch.WorkActivity{Work: workStore},
	}, dispatch.Config{GlobalWorkers: 2, Logger: log, Tenants: tenantSource, Capacity: capacitySource})
	if os.Getenv("AIC_AUTO_DISPATCH") == "off" {
		log.Info("automatic dispatch is off; runs are started by hand")
	} else {
		go dispatcher.Run(ctx)
	}

	// The ranking run: hourly, and once at startup. The base score decides;
	// the model pass is optional and its absence is recorded as a degraded
	// run rather than hidden. The model credential is the same one the pm
	// agent uses — the keychain first, the environment as the fallback.
	var llmRanker ranking.LLMRanker
	if key, err := rankingKey(ctx); err != nil {
		log.Info("the ranking stays on the base score: no model credential", "error", err)
	} else {
		ranker, err := ranking.NewOpenAIRanker(ranking.OpenAIRankerOptions{
			BaseURL: "https://api.openai.com",
			Model:   "gpt-4o-mini",
			APIKey:  key,
		})
		if err != nil {
			log.Warn("the model ranking pass is unavailable", "error", err)
		} else {
			llmRanker = ranking.RunnerAdapter(ranker)
			log.Info("the ranking uses the model pass, with the base score as its fallback")
		}
	}
	go dispatch.NewScheduler(ranking.NewRunner(ranking.NewStore(db), nil, llmRanker), dispatch.Config{
		Tenants: tenantSource,
		Logger:  log,
	}).Run(ctx)

	// The GitHub sync: every two minutes, the design's number. It imports the
	// repositories' issues as items, keeps the linked items' titles and states
	// in step, and completes the item and the run whose pull request was
	// merged.
	go func() {
		syncGitHub := func() {
			tenants, err := tenantSource.DispatchTenants(ctx)
			if err != nil {
				log.Warn("enumerate tenants for the GitHub sync", "error", err)
				return
			}
			for _, tenant := range tenants {
				sc := tenant.Scope
				ref, err := ghsync.CredentialRef(ctx, db, sc)
				if err != nil {
					continue // this tenant has no GitHub credential
				}
				token, err := (integration.Credentials{}).Resolve(ctx, ref)
				if err != nil {
					continue
				}
				svc := ghsync.New(ghsync.Deps{
					DB: db, GitHub: ghsync.TokenClient{Token: token}, Work: workStore,
					Workflows: workflowStore, Logger: log,
				})
				products, err := workStore.ListProducts(ctx, sc)
				if err != nil {
					continue
				}
				for _, product := range products {
					if strings.TrimSpace(product.RepoURL) == "" {
						continue
					}
					for _, op := range []func(context.Context, tenancy.Scope, string, string) (ghsync.Result, error){
						svc.ImportIssues, svc.SyncIssues, svc.SyncMergedPullRequests,
					} {
						result, err := op(ctx, sc, product.ID, product.RepoURL)
						if err != nil {
							log.Warn("GitHub sync", "product", product.Name, "error", err)
							continue
						}
						if len(result.ImportedItems)+len(result.UpdatedTitles)+len(result.CompletedItems)+len(result.CompletedRuns) > 0 {
							log.Info("GitHub sync", "product", product.Name,
								"imported", len(result.ImportedItems), "retitled", len(result.UpdatedTitles),
								"completed_items", len(result.CompletedItems), "completed_runs", len(result.CompletedRuns))
						}
					}
				}
			}
		}
		syncGitHub()
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				syncGitHub()
			}
		}
	}()

	// The log partitions are pruned once a day: a partition whose range ended
	// before the retention window is detached and dropped, and nothing else is
	// touched.
	go func() {
		prune := func() {
			result, err := retention.Prune(ctx, db, retention.Config{KeepDays: 90, Logger: log})
			if err != nil {
				log.Warn("prune job log partitions", "error", err)
				return
			}
			if len(result.Dropped) > 0 {
				log.Info("dropped job log partitions", "count", len(result.Dropped))
			}
		}
		prune()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				prune()
			}
		}
	}()

	srv := &http.Server{
		Addr: *addr,
		Handler: api.New(db, log,
			api.WithSecureCookies(*secureCookies),
			api.WithAgentRuntime(agentStore, agentHub),
			api.WithAgentWorker(worker),
			api.WithWorkflowStores(workflowStore, memoryStore, notifyStore),
			api.WithWorkflowEngine(engine),
		).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", *addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	// Give in-flight requests a bounded window; the orchestrator's own drain
	// (stop claiming work, finish or park running items) comes in M1.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// rankingKey resolves the model credential for the ranking pass: the login
// keychain first (the service "ai-company", the account "openai:default"),
// then the environment variable the pm agent already uses. The secret is
// never written to the database or to a log line.
func rankingKey(ctx context.Context) (string, error) {
	if key, err := (integration.Credentials{}).Resolve(ctx, "ai-company/openai:default"); err == nil && key != "" {
		return key, nil
	}
	if key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); key != "" {
		return key, nil
	}
	return "", errors.New("no model credential in the keychain or the environment")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func logLevel() slog.Level {
	switch os.Getenv("AIC_LOG_LEVEL") {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
