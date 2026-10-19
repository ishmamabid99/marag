package gitprovider

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
)

// GitHub is a GitProvider backed by the GitHub REST API.
type GitHub struct {
	client *http.Client
}

// NewGitHub constructs the backend (client may be nil → http.DefaultClient).
func NewGitHub(client *http.Client) *GitHub { return &GitHub{client: client} }

func (g *GitHub) base(repo RepoRef) string {
	if repo.APIBaseURL != "" {
		return repo.APIBaseURL
	}
	return "https://api.github.com"
}

func (g *GitHub) headers(repo RepoRef) map[string]string {
	return map[string]string{"Authorization": "Bearer " + repo.Token}
}

func (g *GitHub) GetDefaultBranch(ctx context.Context, repo RepoRef) (string, error) {
	var out struct {
		DefaultBranch string `json:"default_branch"`
	}
	url := fmt.Sprintf("%s/repos/%s/%s", g.base(repo), repo.Owner, repo.Name)
	if err := doJSON(ctx, g.client, http.MethodGet, url, g.headers(repo), nil, &out); err != nil {
		return "", err
	}
	return out.DefaultBranch, nil
}

func (g *GitHub) CreatePR(ctx context.Context, repo RepoRef, pr PRRequest) (PRResult, error) {
	body := map[string]any{"title": pr.Title, "body": pr.Body, "head": pr.SourceBranch, "base": pr.TargetBranch}
	var out struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	url := fmt.Sprintf("%s/repos/%s/%s/pulls", g.base(repo), repo.Owner, repo.Name)
	if err := doJSON(ctx, g.client, http.MethodPost, url, g.headers(repo), body, &out); err != nil {
		return PRResult{}, err
	}
	return PRResult{ID: strconv.Itoa(out.Number), Number: out.Number, URL: out.HTMLURL}, nil
}

func (g *GitHub) ClosePR(ctx context.Context, repo RepoRef, prID string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%s", g.base(repo), repo.Owner, repo.Name, prID)
	return doJSON(ctx, g.client, http.MethodPatch, url, g.headers(repo), map[string]any{"state": "closed"}, nil)
}

func (g *GitHub) DeleteBranch(ctx context.Context, repo RepoRef, branch string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/git/refs/heads/%s", g.base(repo), repo.Owner, repo.Name, branch)
	return doJSON(ctx, g.client, http.MethodDelete, url, g.headers(repo), nil, nil)
}
