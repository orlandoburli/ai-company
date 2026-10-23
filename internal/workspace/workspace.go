// Package workspace provisions isolated git worktrees from app-owned bare
// mirrors. It never uses an operator's checkout unless its path is explicitly
// supplied as the source for creating a mirror.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

var (
	// ErrNoMirror means neither a mirror nor a source from which to create one exists.
	ErrNoMirror = errors.New("workspace: no mirror")
	// ErrMergeConflict marks a merge that stopped on conflicts. The worktree is
	// deliberately left in place for a human to resolve.
	ErrMergeConflict = errors.New("workspace: merge conflict")
	// ErrNotIntegration means Merge was called for a node worktree.
	ErrNotIntegration = errors.New("workspace: not an integration workspace")
	// ErrUnsafePath means an ID or workspace path could escape its managed root.
	ErrUnsafePath = errors.New("workspace: unsafe path")
)

// Request describes one node worktree, or a run integration worktree.
type Request struct {
	ProductID   string
	SourceURL   string
	RunID       string
	StepRunID   string
	ItemID      string
	Attempt     int
	Branch      string
	Integration bool
}

// Workspace identifies a worktree created by Provider.
type Workspace struct {
	Dir        string
	Branch     string
	MirrorPath string
	IsPrimary  bool
}

// Provider manages mirrors and worktrees below one application support root.
type Provider struct {
	supportDir string
	gitBin     string
	mu         sync.Mutex
	locks      map[string]*keyedLock
	created    map[string]map[string]struct{}
}

// NewProvider creates a provider. The default support directory is
// ~/Library/Application Support/AICompany on macOS and ~/.local/share/ai-company
// elsewhere.
func NewProvider(opts ...Option) *Provider {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	support := filepath.Join(home, ".local", "share", "ai-company")
	if runtime.GOOS == "darwin" {
		support = filepath.Join(home, "Library", "Application Support", "AICompany")
	}
	p := &Provider{supportDir: support, gitBin: "git", locks: map[string]*keyedLock{}, created: map[string]map[string]struct{}{}}
	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}
	if abs, err := filepath.Abs(p.supportDir); err == nil {
		p.supportDir = filepath.Clean(abs)
	}
	if p.gitBin == "" {
		p.gitBin = "git"
	}
	return p
}

// Option configures a Provider.
type Option func(*Provider)

// WithSupportDir overrides the application support root.
func WithSupportDir(dir string) Option { return func(p *Provider) { p.supportDir = dir } }

// WithGitBin overrides the git executable (default "git").
func WithGitBin(bin string) Option { return func(p *Provider) { p.gitBin = bin } }

// MirrorPath returns the expected bare mirror path for a product.
func (p *Provider) MirrorPath(productID string) string {
	return filepath.Join(p.supportDir, "repos", productID+".git")
}

// Prepare ensures a bare mirror exists and creates a fresh worktree for a node
// execution or run integration branch.
func (p *Provider) Prepare(ctx context.Context, req Request) (Workspace, error) {
	if !safeID(req.ProductID) || !safeID(req.RunID) ||
		(req.StepRunID != "" && !safeID(req.StepRunID)) ||
		(req.ItemID != "" && !safeID(req.ItemID)) {
		return Workspace{}, fmt.Errorf("%w: invalid workspace identifier", ErrUnsafePath)
	}
	if req.Integration {
		if req.Branch != "" && req.Branch != "aic/"+sanitizeComponent(req.RunID) {
			return Workspace{}, fmt.Errorf("%w: integration branch is fixed", ErrUnsafePath)
		}
	} else if req.StepRunID == "" {
		return Workspace{}, fmt.Errorf("workspace: step run id is required")
	}

	mirror := p.MirrorPath(req.ProductID)
	if !p.inSupport(mirror) {
		return Workspace{}, fmt.Errorf("%w: mirror path", ErrUnsafePath)
	}
	unlock := p.lock(mirror)
	defer unlock()

	if err := p.ensureMirror(ctx, mirror, req.SourceURL); err != nil {
		return Workspace{}, err
	}

	branch := req.Branch
	leaf := req.StepRunID
	if req.Integration {
		branch = "aic/" + sanitizeComponent(req.RunID)
		leaf = "__integration__"
	} else if branch == "" {
		branch = fmt.Sprintf("aic/%s-%s-%d", sanitizeComponent(req.ItemID), sanitizeComponent(req.StepRunID), req.Attempt)
	}
	if !validBranch(branch) {
		return Workspace{}, fmt.Errorf("%w: invalid branch name", ErrUnsafePath)
	}

	runDir := filepath.Join(p.supportDir, "runs", req.RunID)
	dir := filepath.Join(runDir, leaf)
	if !p.inRuns(dir) {
		return Workspace{}, fmt.Errorf("%w: worktree path", ErrUnsafePath)
	}
	if err := mkdirManaged(runDir, filepath.Join(p.supportDir, "runs")); err != nil {
		return Workspace{}, err
	}
	if _, err := os.Lstat(dir); err == nil {
		return Workspace{}, fmt.Errorf("workspace: worktree directory already exists: %s", dir)
	} else if !os.IsNotExist(err) {
		return Workspace{}, fmt.Errorf("workspace: inspect worktree directory: %w", err)
	}

	// Git accepts worktree options before --, which terminates option parsing
	// before the absolute path. This works directly against a bare mirror.
	if _, err := p.run(ctx, nil, "-C", mirror, "worktree", "add", "-b", branch, "--", dir); err != nil {
		return Workspace{}, err
	}
	p.recordBranch(mirror, branch)
	return Workspace{Dir: dir, Branch: branch, MirrorPath: mirror, IsPrimary: req.Integration}, nil
}

// Merge merges a node branch into the run integration worktree.
func (p *Provider) Merge(ctx context.Context, ws Workspace, branch string) error {
	if !ws.IsPrimary || filepath.Base(ws.Dir) != "__integration__" {
		return ErrNotIntegration
	}
	if !p.validWorkspace(ws) || !validBranch(branch) {
		return fmt.Errorf("%w: invalid workspace or branch", ErrUnsafePath)
	}
	unlock := p.lock(ws.MirrorPath)
	defer unlock()
	_, err := p.run(ctx, nil, "-C", ws.Dir, "merge", "--no-ff", "--no-edit", branch)
	if err == nil {
		return nil
	}
	var ge *GitError
	if errors.As(err, &ge) && isMergeConflict(ge.Stderr+"\n"+ge.Stdout) {
		return fmt.Errorf("%w: %w", ErrMergeConflict, ge)
	}
	return err
}

// Remove removes one managed worktree and, for an app-owned aic/ branch,
// deletes that branch from its mirror.
func (p *Provider) Remove(ctx context.Context, ws Workspace) error {
	if !p.validWorkspace(ws) {
		return fmt.Errorf("%w: invalid workspace", ErrUnsafePath)
	}
	unlock := p.lock(ws.MirrorPath)
	defer unlock()
	worktrees, err := p.worktreeBranches(ctx, ws.MirrorPath)
	if err != nil {
		return err
	}
	actualBranch := ""
	for dir, branch := range worktrees {
		resolvedDir, okDir := resolvedPath(dir)
		resolvedWorkspace, okWorkspace := resolvedPath(ws.Dir)
		if okDir && okWorkspace && resolvedDir == resolvedWorkspace {
			actualBranch = branch
			break
		}
	}
	if actualBranch == "" || actualBranch != ws.Branch {
		return fmt.Errorf("%w: workspace branch does not match mirror", ErrUnsafePath)
	}
	return p.removeWorktree(ctx, ws.MirrorPath, ws.Dir, actualBranch)
}

// RemoveRun prunes every registered worktree for runID and removes only that
// run's directory beneath the managed runs root.
func (p *Provider) RemoveRun(ctx context.Context, runID string) error {
	if !safeID(runID) {
		return fmt.Errorf("%w: invalid run id", ErrUnsafePath)
	}
	runsRoot := filepath.Join(p.supportDir, "runs")
	if info, err := os.Lstat(runsRoot); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: runs root is not a real directory", ErrUnsafePath)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("workspace: inspect runs root: %w", err)
	}
	runDir := filepath.Join(runsRoot, runID)
	if !p.inRuns(runDir) {
		return fmt.Errorf("%w: run path", ErrUnsafePath)
	}
	reposRoot := filepath.Join(p.supportDir, "repos")
	if info, err := os.Lstat(reposRoot); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: repos root is not a real directory", ErrUnsafePath)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("workspace: inspect repos root: %w", err)
	}
	entries, err := os.ReadDir(reposRoot)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("workspace: list mirrors: %w", err)
	}
	var mirrors []string
	for _, entry := range entries {
		if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && strings.HasSuffix(entry.Name(), ".git") {
			mirror := filepath.Join(reposRoot, entry.Name())
			if p.inSupport(mirror) {
				mirrors = append(mirrors, mirror)
			}
		}
	}
	sort.Strings(mirrors)
	// Keep all mirror locks until pruning and run-directory cleanup complete,
	// preventing a concurrent Prepare from recreating a worktree mid-prune.
	var unlocks []func()
	for _, mirror := range mirrors {
		unlocks = append(unlocks, p.lock(mirror))
	}
	defer func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}()

	if info, err := os.Lstat(runDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: run directory is not a real directory", ErrUnsafePath)
		}
		for _, mirror := range mirrors {
			worktrees, err := p.worktreeBranches(ctx, mirror)
			if err != nil {
				return err
			}
			for dir, branch := range worktrees {
				if pathWithin(dir, runDir) {
					if err := p.removeWorktree(ctx, mirror, dir, branch); err != nil {
						return err
					}
				}
			}
		}
		if err := os.RemoveAll(runDir); err != nil {
			return fmt.Errorf("workspace: remove run directory: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("workspace: inspect run directory: %w", err)
	}
	return nil
}

func (p *Provider) ensureMirror(ctx context.Context, mirror, source string) error {
	if info, err := os.Lstat(mirror); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: mirror is not a directory", ErrUnsafePath)
		}
		out, err := p.run(ctx, nil, "-C", mirror, "rev-parse", "--is-bare-repository")
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(out)) != "true" {
			return fmt.Errorf("workspace: mirror is not bare: %s", mirror)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("workspace: inspect mirror: %w", err)
	}
	if strings.TrimSpace(source) == "" {
		return ErrNoMirror
	}
	if err := mkdirManaged(filepath.Dir(mirror), filepath.Join(p.supportDir, "repos")); err != nil {
		return err
	}
	if _, err := p.run(ctx, nil, "clone", "--bare", "--", source, mirror); err != nil {
		return err
	}
	return nil
}

func (p *Provider) removeWorktree(ctx context.Context, mirror, dir, branch string) error {
	if !p.inSupport(mirror) || !p.inRuns(dir) {
		return fmt.Errorf("%w: remove path", ErrUnsafePath)
	}
	if _, err := p.run(ctx, nil, "-C", mirror, "worktree", "remove", "--force", dir); err != nil {
		return err
	}
	if p.ownsBranch(mirror, branch) && validBranch(branch) {
		if _, err := p.run(ctx, nil, "-C", mirror, "branch", "-D", branch); err != nil {
			return err
		}
		p.forgetBranch(mirror, branch)
	}
	return nil
}

func (p *Provider) worktreeBranches(ctx context.Context, mirror string) (map[string]string, error) {
	out, err := p.run(ctx, nil, "-C", mirror, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	var dir, branch string
	flush := func() {
		if dir != "" {
			result[filepath.Clean(dir)] = strings.TrimPrefix(branch, "refs/heads/")
		}
		dir, branch = "", ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "worktree ") {
			dir = strings.TrimPrefix(line, "worktree ")
		} else if strings.HasPrefix(line, "branch ") {
			branch = strings.TrimPrefix(line, "branch ")
		}
	}
	flush()
	return result, nil
}

func (p *Provider) validWorkspace(ws Workspace) bool {
	if !p.inSupport(ws.MirrorPath) || !p.inRuns(ws.Dir) || ws.Branch == "" || !validBranch(ws.Branch) {
		return false
	}
	rel, err := filepath.Rel(filepath.Join(p.supportDir, "runs"), ws.Dir)
	return err == nil && len(strings.Split(filepath.Clean(rel), string(filepath.Separator))) >= 2
}

func (p *Provider) ownsBranch(mirror, branch string) bool {
	p.mu.Lock()
	_, tracked := p.created[mirror][branch]
	p.mu.Unlock()
	// The namespace is the persistent ownership marker used after a provider
	// restart; explicit non-aic branches are removable only while tracked.
	return tracked || strings.HasPrefix(branch, "aic/")
}

func (p *Provider) inSupport(path string) bool {
	return pathWithin(path, p.supportDir)
}

func (p *Provider) inRuns(path string) bool {
	return pathWithin(path, filepath.Join(p.supportDir, "runs"))
}

func pathWithin(path, root string) bool {
	path, okPath := resolvedPath(path)
	root, okRoot := resolvedPath(root)
	if !okPath || !okRoot {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// resolvedPath evaluates symlinks in the existing prefix and then appends the
// non-existent suffix. Git may report /private/... while macOS APIs supplied
// /var/...; comparing resolved paths handles those aliases without accepting a
// symlink that escapes a managed root.
func resolvedPath(path string) (string, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	abs = filepath.Clean(abs)
	current := abs
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func safeID(id string) bool {
	return id != "" && id != "." && !strings.Contains(id, "..") &&
		!strings.ContainsAny(id, "/\\\x00") && !filepath.IsAbs(id)
}

func sanitizeComponent(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), ".-")
	if out == "" {
		return "x"
	}
	return out
}

func validBranch(branch string) bool {
	if branch == "" || strings.HasPrefix(branch, "-") || strings.Contains(branch, "..") || strings.ContainsAny(branch, " ~^:?*[\\\x00") || strings.HasSuffix(branch, ".") || strings.HasSuffix(branch, "/") || strings.Contains(branch, "//") {
		return false
	}
	for _, r := range branch {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_. /", r)) {
			return false
		}
	}
	return true
}

func isMergeConflict(output string) bool {
	return strings.Contains(output, "CONFLICT (") || strings.Contains(output, "Automatic merge failed") || strings.Contains(output, "Fix conflicts and then commit the result")
}

func mkdirManaged(dir, root string) error {
	if !pathWithin(dir, root) {
		return fmt.Errorf("%w: managed directory", ErrUnsafePath)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("workspace: create managed root: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("workspace: create managed directory: %w", err)
	}
	for current := dir; pathWithin(current, root); current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("workspace: inspect managed directory: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: managed directory is not a real directory", ErrUnsafePath)
		}
		if filepath.Clean(current) == filepath.Clean(root) {
			break
		}
	}
	return nil
}
