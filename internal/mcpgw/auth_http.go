package mcpgw

import (
	"net/http"
	"strings"

	"github.com/fin/multi-agent-rag/internal/mcpauth"
)

const bearerPrefix = "bearer " // matched case-insensitively

// BearerAuth authenticates the Authorization: Bearer <token> header on each
// request via store and injects the caller's scope into the request context for
// ScopeGate. Deny-by-default: a missing/invalid token yields scope "" (all
// tools/call denied), but the request still proceeds so initialize/tools/list
// respond. Never logs the token.
func BearerAuth(store mcpauth.TokenStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scope := ""
		auth := r.Header.Get("Authorization")
		if len(auth) > len(bearerPrefix) && strings.EqualFold(auth[:len(bearerPrefix)], bearerPrefix) {
			token := strings.TrimSpace(auth[len(bearerPrefix):])
			if p, err := store.Authenticate(r.Context(), token); err == nil {
				scope = p.Scope
			}
		}
		next.ServeHTTP(w, r.WithContext(WithScope(r.Context(), scope)))
	})
}
