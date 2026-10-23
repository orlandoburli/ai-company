package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/workflow"
)

// ensureSeedWorkflowTemplates publishes the seeded graphs on a fresh and an
// existing dev tenant alike.
//
// The seed never overwrites what a human owns: a template that already exists
// under the same name is left exactly as it is, published or not. The graphs
// themselves are built in templates.go and validated by the workflow package
// before anything is written, so a broken graph can never reach the database.
func ensureSeedWorkflowTemplates(ctx context.Context, db *sql.DB, ts *tenancy.Store, email, slug string, log *slog.Logger) error {
	sc, err := seedScope(ctx, ts, email, slug)
	if err != nil {
		return err
	}

	agents := agent.NewStore(db)
	ids := map[string]string{}
	for _, name := range []string{"pm", "dev", "reviewer", "qa"} {
		a, err := agents.GetAgentByName(ctx, sc, name)
		if err != nil {
			if errors.Is(err, agent.ErrNotFound) {
				// The agent is missing (a tenant seeded before the agent
				// existed): the templates that need it cannot be published.
				log.Info("seed template skipped, agent missing", "agent", name)
				continue
			}
			return fmt.Errorf("find agent %s: %w", name, err)
		}
		ids[name] = a.ID
	}
	if len(ids) == 0 {
		return nil
	}

	templates, err := seedTemplates(ids)
	if err != nil {
		return fmt.Errorf("build seed templates: %w", err)
	}

	store := workflow.NewStore(db)
	existing, err := store.ListTemplates(ctx, sc, false)
	if err != nil {
		return fmt.Errorf("list templates: %w", err)
	}
	byName := map[string]workflow.TemplateRow{}
	for _, tpl := range existing {
		byName[tpl.Name] = tpl
	}

	for _, tpl := range templates {
		if _, ok := byName[tpl.Name]; ok {
			log.Info("seed template already exists", "template", tpl.Name)
			continue
		}
		row, err := store.CreateTemplate(ctx, sc, workflow.NewTemplate{
			Name:           tpl.Name,
			AppliesTo:      tpl.AppliesTo,
			Graph:          tpl.Graph,
			MaxConcurrency: tpl.MaxConcurrency,
			CreatedBy:      sc.UserID,
		})
		if err != nil {
			return fmt.Errorf("create template %s: %w", tpl.Name, err)
		}
		published, err := store.PublishTemplate(ctx, sc, row.ID)
		if err != nil {
			return fmt.Errorf("publish template %s: %w", tpl.Name, err)
		}
		log.Info("seed template published", "template", published.Name, "version", published.Version)
	}
	return nil
}

// seedScope resolves the tenant scope of the seeded tenant.
func seedScope(ctx context.Context, ts *tenancy.Store, email, slug string) (tenancy.Scope, error) {
	user, err := ts.FindUserByEmail(ctx, email)
	if err != nil {
		return tenancy.Scope{}, fmt.Errorf("find seed user: %w", err)
	}
	memberships, err := ts.MembershipsForUser(ctx, user.ID)
	if err != nil {
		return tenancy.Scope{}, fmt.Errorf("list memberships: %w", err)
	}
	for _, m := range memberships {
		if m.TenantSlug == slug {
			return tenancy.Scope{TenantID: m.TenantID, UserID: user.ID, Role: tenancy.RoleOwner}, nil
		}
	}
	return tenancy.Scope{}, fmt.Errorf("user %s is not a member of tenant %q", email, slug)
}
