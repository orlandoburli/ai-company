package api

import (
	"net/http"

	"github.com/orlandoburli/ai-company/internal/ghsync"
	"github.com/orlandoburli/ai-company/internal/workflow"
)

// githubLinkView is the wire shape of one GitHub link: the issue or the pull
// request an item carries, with what is needed to show it.
type githubLinkView struct {
	Kind       string `json:"kind"`
	Repo       string `json:"repo"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	State      string `json:"state"`
	HeadBranch string `json:"head_branch"`
	BaseBranch string `json:"base_branch"`
}

// handleGitHubLinks lists the GitHub links of an item: the issues it owns and
// the pull requests that mention it. An item may carry many of each.
//
// Reads are open to every role, like the item itself.
func (s *Server) handleGitHubLinks(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	itemID := r.PathValue("id")
	// The item of another tenant is not there at all: the same answer as for
	// the item itself, so the endpoint leaks nothing about other tenants.
	if _, err := s.work.GetItem(r.Context(), sc, itemID); err != nil {
		s.writeWorkError(w, err)
		return
	}
	svc := ghsync.New(ghsync.Deps{DB: s.db, Work: s.work, Workflows: workflow.NewStore(s.db), Logger: s.log})
	links, err := svc.ListItemLinks(r.Context(), sc, itemID)
	if err != nil {
		s.writeGitHubError(w, err)
		return
	}
	views := make([]githubLinkView, 0, len(links))
	for _, link := range links {
		views = append(views, githubLinkView{
			Kind:       link.Kind,
			Repo:       link.Repo,
			Number:     link.IssueNumber,
			Title:      link.IssueTitle,
			URL:        link.IssueURL,
			State:      link.IssueState,
			HeadBranch: link.HeadBranch,
			BaseBranch: link.BaseBranch,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": views})
}
