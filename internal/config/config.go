// Package config loads platform configuration, Consul-driven (Viper remote) to
// match the orchestration-api house pattern: one JSON blob at one Consul KV key,
// gated by the CONSUL env var, else a local JSON file. mapstructure + json tags.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/url"
	"os"
	"strconv"
	"sync"

	"github.com/spf13/viper"
	_ "github.com/spf13/viper/remote" // registers the consul remote provider (init)
)

// Config is the whole-platform configuration + secrets (one Consul JSON blob).
type Config struct {
	Database  DatabaseConfig  `mapstructure:"database" json:"database"`
	Kafka     KafkaConfig     `mapstructure:"kafka" json:"kafka"`
	Git       GitConfig       `mapstructure:"git" json:"git"`
	Anthropic AnthropicConfig `mapstructure:"anthropic" json:"anthropic"`
	Linear    LinearConfig    `mapstructure:"linear" json:"linear"`
	// Roles overrides per-agent LLM params (model/max_tokens/thinking), keyed by
	// role name (e.g. "system-architect", "developer"). Unset roles/fields use
	// the built-in defaults (worker.DefaultRoster).
	Roles         map[string]RoleConfig `mapstructure:"roles" json:"roles"`
	Embeddings    EmbeddingsConfig      `mapstructure:"embeddings" json:"embeddings"`
	Reranker      RerankerConfig        `mapstructure:"reranker" json:"reranker"`
	Registry      RegistryConfig        `mapstructure:"registry" json:"registry"`
	Worker        WorkerConfig          `mapstructure:"worker" json:"worker"`
	ManagedAgents MAConfig              `mapstructure:"managed_agents" json:"managed_agents"`
	Consul        ConsulControl         `mapstructure:"consul" json:"consul"`
}

// DatabaseConfig is the shared Postgres connection (single instance). Prefer the
// structured fields (host/port/user/password/name/sslmode) — the backend builds
// the DSN and URL-encodes the password, so special chars (from e.g. `openssl
// rand -base64`) need no manual escaping. `url`, if set, overrides the parts
// (back-compat / full-DSN escape hatch).
type DatabaseConfig struct {
	URL      string `mapstructure:"url" json:"url"`
	Host     string `mapstructure:"host" json:"host"`
	Port     int    `mapstructure:"port" json:"port"`
	User     string `mapstructure:"user" json:"user"`
	Password string `mapstructure:"password" json:"password"`
	Name     string `mapstructure:"name" json:"name"`
	SSLMode  string `mapstructure:"sslmode" json:"sslmode"`
}

// DSN returns the Postgres connection string. If URL is set it is returned
// verbatim; otherwise a postgres:// URL is assembled from the parts with the
// password percent-encoded. Returns "" when neither URL nor Host is set.
func (d DatabaseConfig) DSN() string {
	if d.URL != "" {
		return d.URL
	}
	if d.Host == "" {
		return ""
	}
	port := d.Port
	if port == 0 {
		port = 5432
	}
	ssl := d.SSLMode
	if ssl == "" {
		ssl = "disable"
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(d.User, d.Password), // percent-encodes the password
		Host:     net.JoinHostPort(d.Host, strconv.Itoa(port)),
		Path:     "/" + d.Name,
		RawQuery: "sslmode=" + url.QueryEscape(ssl),
	}
	return u.String()
}

// KafkaConfig is the broker list + SASL/TLS auth + client tuning (house shape).
type KafkaConfig struct {
	Brokers             []string `mapstructure:"brokers" json:"brokers"`
	Username            string   `mapstructure:"username" json:"username"`
	Password            string   `mapstructure:"password" json:"password"`
	TLSEnabled          bool     `mapstructure:"tls_enabled" json:"tls_enabled"`
	ProducerCompression string   `mapstructure:"producer_compression" json:"producer_compression"`
	ProducerMaxRetries  int      `mapstructure:"producer_max_retries" json:"producer_max_retries"`
	ConsumerMaxRetries  int      `mapstructure:"consumer_max_retries" json:"consumer_max_retries"`
}

// GitConfig holds git-hosting tokens (secrets).
type GitConfig struct {
	GitHubToken string `mapstructure:"github_token" json:"github_token"`
	GitLabToken string `mapstructure:"gitlab_token" json:"gitlab_token"`
}

// AnthropicConfig configures the Claude backend: a direct API key, or Google
// Vertex AI (funded by GCP), with config-driven model overrides + feature
// toggles for provider parity gaps.
type AnthropicConfig struct {
	APIKey                  string            `mapstructure:"api_key" json:"api_key"`
	Provider                string            `mapstructure:"provider" json:"provider"` // "api" (default) | "vertex"
	VertexProject           string            `mapstructure:"vertex_project" json:"vertex_project"`
	VertexRegion            string            `mapstructure:"vertex_region" json:"vertex_region"`
	ModelOverrides          map[string]string `mapstructure:"model_overrides" json:"model_overrides"` // canonical model id -> provider-specific id
	DisableThinking         bool              `mapstructure:"disable_thinking" json:"disable_thinking"`
	DisableStructuredOutput bool              `mapstructure:"disable_structured_output" json:"disable_structured_output"`
}

// LinearConfig configures the Linear trigger gateway.
type LinearConfig struct {
	Addr          string `mapstructure:"addr" json:"addr"`
	APIKey        string `mapstructure:"api_key" json:"api_key"`
	SigningSecret string `mapstructure:"signing_secret" json:"signing_secret"`
	Trigger       string `mapstructure:"trigger" json:"trigger"`
	// AllowedActors restricts who may trigger/approve via @marag. Entries match
	// the webhook actor's email (case-insensitive), Linear user id, or name.
	// Empty = allow every workspace member (back-compat).
	AllowedActors []string `mapstructure:"allowed_actors" json:"allowed_actors"`
}

// RoleConfig overrides a single agent role's LLM params. Unset fields (empty
// model, zero max_tokens, nil thinking) fall back to the built-in default.
type RoleConfig struct {
	Model     string `mapstructure:"model" json:"model"`
	MaxTokens int    `mapstructure:"max_tokens" json:"max_tokens"`
	Thinking  *bool  `mapstructure:"thinking" json:"thinking"` // pointer: nil = use default
}

// EmbeddingsConfig configures the embedder (empty URL -> in-process fake).
type EmbeddingsConfig struct {
	OllamaURL   string `mapstructure:"ollama_url" json:"ollama_url"`
	OllamaModel string `mapstructure:"ollama_model" json:"ollama_model"`
}

// RerankerConfig configures the reranker (empty URL -> in-process fake).
type RerankerConfig struct {
	URL   string `mapstructure:"url" json:"url"`
	Model string `mapstructure:"model" json:"model"`
	// Kind selects the server API: "tei" (default; HuggingFace TEI /rerank) or
	// "infinity" (michaelf34/infinity — multi-arch, runs native on arm64).
	Kind string `mapstructure:"kind" json:"kind"`
}

// RegistryConfig configures the registry gRPC server.
type RegistryConfig struct {
	GRPCAddr string `mapstructure:"grpc_addr" json:"grpc_addr"`
}

// WorkerConfig selects worker behavior.
type WorkerConfig struct {
	// DeveloperBackend selects the developer stage backend: "local" (default,
	// local sandbox + tool-loop) or "ma" (Managed Agents).
	DeveloperBackend string        `mapstructure:"developer_backend" json:"developer_backend"`
	FakeLLM          bool          `mapstructure:"fake_llm" json:"fake_llm"`
	Sandbox          SandboxConfig `mapstructure:"sandbox" json:"sandbox"`
	// Concurrency bounds how many stage records the worker handles in parallel
	// from one Kafka poll batch (0 ⇒ default). Per-role caps (below) still apply
	// on top of this global ceiling.
	Concurrency int `mapstructure:"concurrency" json:"concurrency"`
	// RoleConcurrency overrides the built-in per-role in-flight caps, keyed by
	// stage role (e.g. "developer": 2). Unset roles use worker.DefaultRoleConcurrency.
	RoleConcurrency map[string]int `mapstructure:"role_concurrency" json:"role_concurrency"`
}

// SandboxConfig controls isolation of the local developer sandbox's untrusted
// command execution (agent run_command + repo `go test`).
type SandboxConfig struct {
	// ExecPrefix is the jail launcher that wraps every untrusted command, e.g.
	// ["bwrap","--unshare-all","--net","none",...] or a firejail/nsjail wrapper.
	// Empty + AllowUnsandboxed=false ⇒ the local backend refuses to start.
	ExecPrefix []string `mapstructure:"exec_prefix" json:"exec_prefix"`
	// AllowUnsandboxed permits direct (unjailed) execution — DEV/TEST ONLY.
	AllowUnsandboxed bool `mapstructure:"allow_unsandboxed" json:"allow_unsandboxed"`
}

// MAConfig configures the Managed Agents developer backend.
type MAConfig struct {
	APIBaseURL         string `mapstructure:"api_base_url" json:"api_base_url"`
	Model              string `mapstructure:"model" json:"model"`
	AgentID            string `mapstructure:"agent_id" json:"agent_id"`
	VaultCredentialRef string `mapstructure:"vault_credential_ref" json:"vault_credential_ref"`
}

// ConsulControl holds Consul watch settings (the address/token come from env).
type ConsulControl struct {
	Watch        bool `mapstructure:"watch" json:"watch"`
	WatchTimeout int  `mapstructure:"watch_timeout" json:"watch_timeout"`
}

// Defaults returns a Config pre-filled with non-secret defaults (secrets empty).
func Defaults() *Config {
	return &Config{
		Kafka:         KafkaConfig{Brokers: []string{"localhost:9092"}, ProducerCompression: "snappy", ProducerMaxRetries: 3, ConsumerMaxRetries: 3},
		Linear:        LinearConfig{Addr: ":8080", Trigger: "@marag"},
		Embeddings:    EmbeddingsConfig{OllamaModel: "nomic-embed-text"},
		Reranker:      RerankerConfig{Model: "bge-reranker-base"},
		Registry:      RegistryConfig{GRPCAddr: ":50051"},
		Worker:        WorkerConfig{DeveloperBackend: "local", FakeLLM: false, Concurrency: 8},
		Anthropic:     AnthropicConfig{Provider: "api"},
		ManagedAgents: MAConfig{Model: "claude-sonnet-5"},
		Consul:        ConsulControl{Watch: false, WatchTimeout: 5},
	}
}

// LoadFile applies defaults then overlays the JSON file at path. A missing file
// yields defaults (no error).
func LoadFile(path string) (*Config, error) {
	c := Defaults()
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		log.Printf("config: %s not found; using defaults", path)
		return c, nil
	}
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("json")
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := v.Unmarshal(c); err != nil {
		return nil, fmt.Errorf("config: unmarshal %s: %w", path, err)
	}
	return c, nil
}

var (
	once sync.Once
	conf *Config
)

func getenv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

// Load reads config per env: CONSUL=true → Consul KV; else the CONFIG_JSON file.
func Load() (*Config, error) {
	if getenv("CONSUL", "false") == "true" {
		return loadConsul()
	}
	return LoadFile(getenv("CONFIG_JSON", "config.json"))
}

// loadConsul reads the JSON config blob from a single Consul KV key.
func loadConsul() (*Config, error) {
	addr := getenv("CONSUL_URL", "127.0.0.1:8500")
	key := getenv("CONSUL_PATH", "multi-agent-rag")
	c := Defaults()
	v := viper.New()
	if err := v.AddRemoteProvider("consul", addr, key); err != nil {
		return nil, fmt.Errorf("config: consul provider: %w", err)
	}
	v.SetConfigType("json")
	if err := v.ReadRemoteConfig(); err != nil {
		return nil, fmt.Errorf("config: read consul %s key %q: %w", addr, key, err)
	}
	if err := v.Unmarshal(c); err != nil {
		return nil, fmt.Errorf("config: unmarshal consul: %w", err)
	}
	return c, nil
}

// Init loads the singleton once.
func Init() error {
	var err error
	once.Do(func() { conf, err = Load() })
	return err
}

// Get returns the config singleton, lazily loading it on first use.
func Get() *Config {
	_ = Init()
	if conf == nil {
		conf = Defaults()
	}
	return conf
}
