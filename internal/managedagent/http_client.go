package managedagent

import (
	"context"
	"fmt"
)

// HTTPMAClient is the real Managed Agents MAClient, backed by
// github.com/anthropics/anthropic-sdk-go's Beta Managed Agents surface
// (client.Beta.{Agents,Environments,Sessions,Vaults,...}).
//
// SDK verification (anthropic-sdk-go v1.58.0, see go.mod): the Beta Managed
// Agents API is real (BetaAgentService, BetaSessionService,
// BetaEnvironmentService, BetaVaultService, BetaSessionEventService with
// Send/StreamEvents, ...). However, it cannot be wired confidently against
// this project's current config surface:
//
//   - BetaSessionNewParams requires an EnvironmentID (api:"required") — the
//     environment defines the session's container. internal/config.MAConfig
//     (APIBaseURL, Model, AgentID, VaultCredentialRef) has no environment id,
//     and BetaManagedAgentsAgent carries no default environment to fall back
//     on, so a session cannot be constructed without inventing where that id
//     comes from.
//   - BetaSessionNewParams.VaultIDs takes Vault IDs (BetaVaultService), not
//     individual credential references. config.ManagedAgents.VaultCredentialRef
//     is a credential reference *within* a vault; mapping it directly to
//     VaultIDs would be guessing at semantics that need a live account to
//     confirm.
//
// Rather than fabricate those mappings, this client is a compile-verified
// skeleton: the constructor and fields are in place so the config-driven
// switch in cmd/worker is fully wired, and RunDeveloperSession returns a
// descriptive error until the session/environment/vault plumbing above is
// finished and validated against a live account.
type HTTPMAClient struct {
	apiKey             string
	apiBaseURL         string
	agentID            string
	vaultCredentialRef string
}

// NewHTTPMAClient constructs the real Managed Agents client.
func NewHTTPMAClient(apiKey, apiBaseURL, agentID, vaultCredentialRef string) *HTTPMAClient {
	return &HTTPMAClient{
		apiKey:             apiKey,
		apiBaseURL:         apiBaseURL,
		agentID:            agentID,
		vaultCredentialRef: vaultCredentialRef,
	}
}

// TODO(MA): wire a real Managed Agents session (compile-verified; live-untested):
//  1. client := anthropic.NewClient(option.WithAPIKey(c.apiKey), option.WithBaseURL(c.apiBaseURL))
//  2. Resolve/create a BetaEnvironmentService environment (needs a config field
//     this project doesn't have yet — e.g. config.ManagedAgents.EnvironmentID)
//     and a BetaVaultService vault id that contains c.vaultCredentialRef.
//  3. client.Beta.Sessions.New(ctx, BetaSessionNewParams{Agent: ..., EnvironmentID: ...,
//     VaultIDs: []string{vaultID}}) to open the session.
//  4. client.Beta.Sessions.Events.Send(ctx, sessionID, ...) with a user.message event
//     instructing the agent to clone req.RemoteURL (via the vault credential),
//     implement req.Spec/req.Design on req.Branch, run
//     `go build ./... && go test ./...` until green, push the branch, and emit a
//     final JSON {branch, head_commit, files_changed, tests_passed, summary}.
//  5. client.Beta.Sessions.Events.StreamEvents(ctx, sessionID, ...) until a
//     session.status_idle/session.status_terminated event, then parse the final
//     agent.message content as the structured MAResult JSON above.
//
// RunDeveloperSession runs one repo's developer session on Managed Agents.
func (c *HTTPMAClient) RunDeveloperSession(_ context.Context, req MASession) (MAResult, error) {
	return MAResult{}, fmt.Errorf("managedagent: HTTPMAClient not implemented (verify anthropic-sdk-go Managed Agents API + validate live): repo %s", req.RepoID)
}
