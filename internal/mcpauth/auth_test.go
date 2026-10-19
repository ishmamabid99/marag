package mcpauth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestHashTokenDeterministicAndDistinct(t *testing.T) {
	h1 := HashToken("abc")
	h2 := HashToken("abc")
	if h1 != h2 {
		t.Fatal("hash not deterministic")
	}
	if HashToken("abc") == HashToken("abd") {
		t.Fatal("distinct tokens must hash differently")
	}
	if HashToken("") == "" {
		t.Fatal("hash of empty string should still be a hex digest")
	}
}

func TestGenerateTokenUniqueAndLong(t *testing.T) {
	a, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	b, _ := GenerateToken()
	if a == b {
		t.Fatal("tokens must be unique")
	}
	if len(a) < 32 {
		t.Fatalf("token too short: %d", len(a))
	}
}

func TestFakeStoreAuthenticate(t *testing.T) {
	s := NewFakeStore()
	s.Add("good-token", "alice")
	ctx := context.Background()

	p, err := s.Authenticate(ctx, "good-token")
	if err != nil {
		t.Fatalf("authenticate good token: %v", err)
	}
	if p.Name != "alice" {
		t.Fatalf("principal = %q, want alice", p.Name)
	}

	if _, err := s.Authenticate(ctx, "bad-token"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("bad token err = %v, want ErrUnauthenticated", err)
	}
	if _, err := s.Authenticate(ctx, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("empty token err = %v, want ErrUnauthenticated", err)
	}
}

func TestFakeStoreExpiry(t *testing.T) {
	s := NewFakeStore()
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	s.AddWithExpiry("expired", "bob", &past)
	s.AddWithExpiry("valid", "carol", &future)
	ctx := context.Background()

	if _, err := s.Authenticate(ctx, "expired"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired token must be denied, got %v", err)
	}
	p, err := s.Authenticate(ctx, "valid")
	if err != nil || p.Name != "carol" {
		t.Fatalf("valid token: p=%v err=%v", p, err)
	}
}

func TestValidScope(t *testing.T) {
	if !ValidScope(ScopeContext) || !ValidScope(ScopeFull) {
		t.Fatal("context and full must be valid scopes")
	}
	if ValidScope("") || ValidScope("admin") {
		t.Fatal("unknown scopes must be invalid")
	}
}

func TestFakeStoreScope(t *testing.T) {
	s := NewFakeStore()
	s.AddScoped("ctx-tok", "reader", ScopeContext, nil)
	s.Add("full-tok", "operator") // Add defaults to full
	ctx := context.Background()
	if p, _ := s.Authenticate(ctx, "ctx-tok"); p.Scope != ScopeContext {
		t.Fatalf("scope = %q want context", p.Scope)
	}
	if p, _ := s.Authenticate(ctx, "full-tok"); p.Scope != ScopeFull {
		t.Fatalf("default scope = %q want full", p.Scope)
	}
}
