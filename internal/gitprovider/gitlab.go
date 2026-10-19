package gitprovider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// GitLab is a GitProvider backed by the GitLab REST API (v4).
type GitLab struct {
	client *http.Client
}

// NewGitLab constructs the backend (client may be nil → http.DefaultClient).
func NewGitLab(client *http.Client) *GitLab { return &GitLab{client: client} }

func (g *GitLab) base(repo RepoRef) string {
	if repo.APIBaseURL != "" {
		return repo.APIBaseURL
	}
	return "https://gitlab.com/api/v4"
}

func (g *GitLab) project(repo RepoRef) string {
	return url.PathEscape(repo.Owner + "/" + repo.Name)
}

func (g *GitLab) headers(repo RepoRef) map[string]string {
	return map[string]string{"PRIVATE-TOKEN": repo.Token}
}

func (g *GitLab) GetDefaultBranch(ctx context.Context, repo RepoRef) (string, error) {
	var out struct {
		DefaultBranch string `json:"default_branch"`
	}
	u := fmt.Sprintf("%s/projects/%s", g.base(repo), g.project(repo))
	if err := doJSON(ctx, g.client, http.MethodGet, u, g.headers(repo), nil, &out); err != nil {
		return "", err
	}
	return out.DefaultBranch, nil
}

func (g *GitLab) CreatePR(ctx context.Context, repo RepoRef, pr PRRequest) (PRResult, error) {
	body := map[string]any{"source_branch": pr.SourceBranch, "target_branch": pr.TargetBranch, "title": pr.Title, "description": pr.Body}
	var out struct {
		IID    int    `json:"iid"`
		WebURL string `json:"web_url"`
	}
	u := fmt.Sprintf("%s/projects/%s/merge_requests", g.base(repo), g.project(repo))
	if err := doJSON(ctx, g.client, http.MethodPost, u, g.headers(repo), body, &out); err != nil {
		return PRResult{}, err
	}
	return PRResult{ID: strconv.Itoa(out.IID), Number: out.IID, URL: out.WebURL}, nil
}

func (g *GitLab) ClosePR(ctx context.Context, repo RepoRef, prID string) error {
	u := fmt.Sprintf("%s/projects/%s/merge_requests/%s", g.base(repo), g.project(repo), prID)
	return doJSON(ctx, g.client, http.MethodPut, u, g.headers(repo), map[string]any{"state_event": "close"}, nil)
}

func (g *GitLab) DeleteBranch(ctx context.Context, repo RepoRef, branch string) error {
	u := fmt.Sprintf("%s/projects/%s/repository/branches/%s", g.base(repo), g.project(repo), branch)
	return doJSON(ctx, g.client, http.MethodDelete, u, g.headers(repo), nil, nil)
}
