// Package skill provides the tenant-scoped, versioned procedure registry.
package skill

import (
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("skill: not found")
	ErrConflict = errors.New("skill: conflict")
	ErrInvalid  = errors.New("skill: invalid")
)

type Skill struct {
	ID             string    `json:"id"`
	TenantID       string    `json:"tenant_id"`
	ProductID      *string   `json:"product_id"`
	Name           string    `json:"name"`
	Slug           string    `json:"slug"`
	Description    string    `json:"description"`
	CurrentVersion int       `json:"current_version"`
	Enabled        bool      `json:"enabled"`
	CreatedBy      *string   `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
}

type Version struct {
	ID         string     `json:"id"`
	SkillID    string     `json:"skill_id"`
	Version    int        `json:"version"`
	ContentMD  string     `json:"content_md"`
	ContentSHA string     `json:"content_sha"`
	AuthorKind string     `json:"author_kind"`
	AuthorID   *string    `json:"author_id"`
	ApprovedBy *string    `json:"approved_by"`
	ApprovedAt *time.Time `json:"approved_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

type NewSkill struct {
	ProductID   *string
	Name        string
	Slug        string
	Description string
}

type Patch struct {
	Name        *string
	Slug        *string
	Description *string
}

type NewVersion struct {
	ContentMD  string
	AuthorKind string // human or agent_draft; empty means human
	AuthorID   *string
}
