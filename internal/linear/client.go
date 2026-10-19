package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Client posts comments to Linear.
type Client interface {
	PostComment(ctx context.Context, issueID, body string) error
}

// PostedComment records a FakeClient call.
type PostedComment struct {
	IssueID string
	Body    string
}

// FakeClient is a Client for tests.
type FakeClient struct{ Comments []PostedComment }

// PostComment records the comment.
func (f *FakeClient) PostComment(_ context.Context, issueID, body string) error {
	f.Comments = append(f.Comments, PostedComment{issueID, body})
	return nil
}

// HTTPClient posts comments via the Linear GraphQL API.
type HTTPClient struct {
	http    *http.Client
	baseURL string
	apiKey  string
}

// NewHTTPClient constructs the client. baseURL defaults to Linear's endpoint when empty.
func NewHTTPClient(httpClient *http.Client, baseURL, apiKey string) *HTTPClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if baseURL == "" {
		baseURL = "https://api.linear.app/graphql"
	}
	return &HTTPClient{http: httpClient, baseURL: baseURL, apiKey: apiKey}
}

const commentMutation = `mutation($id:String!,$body:String!){commentCreate(input:{issueId:$id,body:$body}){success}}`

// PostComment creates a comment on the issue.
func (c *HTTPClient) PostComment(ctx context.Context, issueID, body string) error {
	payload, err := json.Marshal(map[string]any{
		"query":     commentMutation,
		"variables": map[string]any{"id": issueID, "body": body},
	})
	if err != nil {
		return fmt.Errorf("linear: marshal comment: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("linear: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("linear: post comment: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("linear: comment status %d: %s", resp.StatusCode, string(data))
	}
	var out struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Data struct {
			CommentCreate struct {
				Success bool `json:"success"`
			} `json:"commentCreate"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return fmt.Errorf("linear: decode comment response: %w", err)
	}
	if len(out.Errors) > 0 {
		return fmt.Errorf("linear: graphql error: %s", out.Errors[0].Message)
	}
	if !out.Data.CommentCreate.Success {
		return fmt.Errorf("linear: comment create not successful")
	}
	return nil
}
