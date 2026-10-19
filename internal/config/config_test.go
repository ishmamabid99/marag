package config

import (
	"net/url"
	"testing"
)

func TestDatabaseDSN(t *testing.T) {
	// An explicit URL wins verbatim.
	if got := (DatabaseConfig{URL: "postgres://verbatim"}).DSN(); got != "postgres://verbatim" {
		t.Fatalf("explicit URL should win: %q", got)
	}
	// Unconfigured → empty (callers validate on "").
	if got := (DatabaseConfig{}).DSN(); got != "" {
		t.Fatalf("unconfigured DSN should be empty: %q", got)
	}
	// Parts assembled; a password with URL-special chars must round-trip.
	d := DatabaseConfig{Host: "stage.pg.fin.com", Port: 5432, User: "marag",
		Password: "WPq/aB+ed=", Name: "marag", SSLMode: "require"}
	u, err := url.Parse(d.DSN())
	if err != nil {
		t.Fatalf("DSN not parseable: %v (%q)", err, d.DSN())
	}
	if u.Scheme != "postgres" || u.Host != "stage.pg.fin.com:5432" || u.Path != "/marag" {
		t.Fatalf("DSN parts wrong: %q", d.DSN())
	}
	if pw, _ := u.User.Password(); u.User.Username() != "marag" || pw != "WPq/aB+ed=" {
		t.Fatalf("user/password didn't round-trip: %q / %q", u.User.Username(), pw)
	}
	if u.Query().Get("sslmode") != "require" {
		t.Fatalf("sslmode wrong: %q", u.Query().Get("sslmode"))
	}
	// Defaults: port 5432, sslmode disable.
	if got := (DatabaseConfig{Host: "h", User: "u", Password: "p", Name: "n"}).DSN(); got != "postgres://u:p@h:5432/n?sslmode=disable" {
		t.Fatalf("defaults DSN = %q", got)
	}
}

func TestDefaults(t *testing.T) {
	c := Defaults()
	if c.Worker.DeveloperBackend != "local" {
		t.Fatalf("default developer_backend = %q, want local", c.Worker.DeveloperBackend)
	}
	if c.ManagedAgents.Model != "claude-sonnet-5" {
		t.Fatalf("default MA model = %q", c.ManagedAgents.Model)
	}
	if c.Consul.WatchTimeout != 5 {
		t.Fatalf("default watch timeout = %d, want 5", c.Consul.WatchTimeout)
	}
	if c.Worker.FakeLLM != false {
		t.Fatalf("default fake_llm = %v, want false", c.Worker.FakeLLM)
	}
	if c.Linear.Trigger != "@marag" {
		t.Fatalf("default linear trigger = %q, want @marag", c.Linear.Trigger)
	}
	if c.Registry.GRPCAddr != ":50051" {
		t.Fatalf("default registry grpc_addr = %q, want :50051", c.Registry.GRPCAddr)
	}
	if c.Embeddings.OllamaModel != "nomic-embed-text" {
		t.Fatalf("default embeddings ollama_model = %q, want nomic-embed-text", c.Embeddings.OllamaModel)
	}
	if len(c.Kafka.Brokers) != 1 || c.Kafka.Brokers[0] != "localhost:9092" {
		t.Fatalf("default kafka brokers = %v, want [localhost:9092]", c.Kafka.Brokers)
	}
	if c.Kafka.ProducerCompression != "snappy" {
		t.Fatalf("default kafka producer_compression = %q, want snappy", c.Kafka.ProducerCompression)
	}
	if c.Kafka.Username != "" {
		t.Fatalf("default kafka username = %q, want empty", c.Kafka.Username)
	}
	if c.Git.GitHubToken != "" {
		t.Fatalf("default github_token = %q, want empty", c.Git.GitHubToken)
	}
	if c.Anthropic.APIKey != "" {
		t.Fatalf("default anthropic api_key = %q, want empty", c.Anthropic.APIKey)
	}
	if c.Anthropic.Provider != "api" {
		t.Fatalf("default anthropic provider = %q, want api", c.Anthropic.Provider)
	}
	if c.Worker.Sandbox.AllowUnsandboxed {
		t.Fatalf("default sandbox allow_unsandboxed = true, want false (fail-closed)")
	}
}

func TestLoadFileOverlaysDefaults(t *testing.T) {
	c, err := LoadFile("testdata/override.json")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	// Overridden by the file.
	if c.Database.URL != "postgres://u:p@db:5432/x" {
		t.Fatalf("database.url = %q", c.Database.URL)
	}
	if len(c.Kafka.Brokers) != 1 || c.Kafka.Brokers[0] != "broker:9092" {
		t.Fatalf("kafka.brokers = %v", c.Kafka.Brokers)
	}
	if c.Kafka.Username != "ku" {
		t.Fatalf("kafka.username = %q", c.Kafka.Username)
	}
	if c.Git.GitHubToken != "ght" {
		t.Fatalf("git.github_token = %q", c.Git.GitHubToken)
	}
	if c.Worker.DeveloperBackend != "ma" {
		t.Fatalf("worker.developer_backend = %q", c.Worker.DeveloperBackend)
	}
	if c.Worker.FakeLLM != true {
		t.Fatalf("worker.fake_llm = %v", c.Worker.FakeLLM)
	}
	if c.Linear.APIKey != "lk" {
		t.Fatalf("linear.api_key = %q", c.Linear.APIKey)
	}
	if c.Anthropic.Provider != "vertex" {
		t.Fatalf("anthropic.provider = %q", c.Anthropic.Provider)
	}
	if c.Anthropic.ModelOverrides["claude-sonnet-5"] != "claude-sonnet-4-5@x" {
		t.Fatalf("anthropic.model_overrides = %v", c.Anthropic.ModelOverrides)
	}
	// Not in the file → default retained.
	if c.Linear.Trigger != "@marag" {
		t.Fatalf("default not retained: linear.trigger = %q", c.Linear.Trigger)
	}
}

func TestLoadFileMissingIsDefaults(t *testing.T) {
	c, err := LoadFile("testdata/does-not-exist.json")
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if c.Worker.DeveloperBackend != "local" {
		t.Fatalf("missing file should yield defaults, got %+v", c.Worker)
	}
}

func TestLoadEnvGatedFile(t *testing.T) {
	t.Setenv("CONSUL", "false")
	t.Setenv("CONFIG_JSON", "testdata/override.json")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Worker.DeveloperBackend != "ma" {
		t.Fatalf("env-gated file load = %+v", c.Worker)
	}
}

func TestRepoConfigJSONParses(t *testing.T) {
	c, err := LoadFile("../../config.json")
	if err != nil {
		t.Fatalf("repo config.json must parse: %v", err)
	}
	// Sanity: sections present, no secrets committed.
	if c.Worker.DeveloperBackend == "" || c.Database.URL == "" || len(c.Kafka.Brokers) == 0 {
		t.Fatalf("config.json missing core sections: %+v", c)
	}
	if c.Git.GitHubToken != "" || c.Anthropic.APIKey != "" || c.Linear.SigningSecret != "" {
		t.Fatalf("config.json must NOT commit secret values")
	}
}

func TestGetSingleton(t *testing.T) {
	t.Setenv("CONSUL", "false")
	t.Setenv("CONFIG_JSON", "testdata/does-not-exist.json") // → defaults
	a := Get()
	b := Get()
	if a != b {
		t.Fatalf("Get should return the same singleton pointer")
	}
	if a.Worker.DeveloperBackend != "local" {
		t.Fatalf("singleton defaults = %+v", a.Worker)
	}
}
