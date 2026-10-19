package models

import (
	"strings"
	"testing"
	"time"
)

func TestGitProviderValid(t *testing.T) {
	tests := []struct {
		in   GitProvider
		want bool
	}{
		{GitProviderGitHub, true},
		{GitProviderGitLab, true},
		{GitProvider("bitbucket"), false},
		{GitProvider(""), false},
	}
	for _, tt := range tests {
		if got := tt.in.Valid(); got != tt.want {
			t.Errorf("GitProvider(%q).Valid() = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestRepoValidate(t *testing.T) {
	base := Repo{
		ID:             "repo-abc",
		Name:           "acme/web",
		Provider:       GitProviderGitHub,
		RemoteURL:      "https://github.com/acme/web",
		DefaultBranch:  "main",
		EmbeddingModel: "nomic-embed-text",
		EmbeddingDim:   768,
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid repo returned error: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Repo)
	}{
		{"empty id", func(r *Repo) { r.ID = "" }},
		{"empty name", func(r *Repo) { r.Name = "" }},
		{"bad provider", func(r *Repo) { r.Provider = "svn" }},
		{"empty remote", func(r *Repo) { r.RemoteURL = "" }},
		{"empty branch", func(r *Repo) { r.DefaultBranch = "" }},
		{"empty model", func(r *Repo) { r.EmbeddingModel = "" }},
		{"zero dim", func(r *Repo) { r.EmbeddingDim = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := base
			tt.mutate(&r)
			if err := r.Validate(); err == nil {
				t.Errorf("expected error for %s, got nil", tt.name)
			}
		})
	}
}

func TestRepoIsStale(t *testing.T) {
	now := time.Now()
	if !(Repo{}).IsStale() {
		t.Error("never-ingested repo should be stale")
	}
	if (Repo{LastIngestedAt: &now, CommitsBehind: 0}).IsStale() {
		t.Error("fresh repo should not be stale")
	}
	if !(Repo{LastIngestedAt: &now, CommitsBehind: 3}).IsStale() {
		t.Error("behind repo should be stale")
	}
}

func TestRepoPartitionName(t *testing.T) {
	got := Repo{ID: "acme/Web-Service.v2"}.PartitionName()
	if !strings.HasPrefix(got, "code_embeddings_") {
		t.Errorf("partition %q missing prefix", got)
	}
	// Must be a valid unquoted SQL identifier: lowercase letters, digits, underscore.
	for _, c := range got {
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			t.Errorf("partition %q contains illegal char %q", got, c)
		}
	}
	// Deterministic.
	if (Repo{ID: "acme/Web-Service.v2"}).PartitionName() != got {
		t.Error("PartitionName not deterministic")
	}
	// Distinct ids -> distinct partitions.
	if (Repo{ID: "a"}).PartitionName() == (Repo{ID: "b"}).PartitionName() {
		t.Error("distinct ids collided")
	}
}
