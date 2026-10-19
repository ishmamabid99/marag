// Package skills provides compiled-in per-agent skill rulesets injected into
// each pipeline role's system prompt. A shared base applies to all agents; each
// role layers a domain-specific skill on top.
package skills

import (
	"embed"
	"strings"
)

//go:embed library/*.md
var libFS embed.FS

const sep = "\n\n---\n\n"

func load(name string) string {
	b, err := libFS.ReadFile("library/" + name + ".md")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Base returns the shared ruleset applied to every agent.
func Base() string { return load("base") }

// For returns the base + role-specific ruleset (role skill omitted if absent).
func For(role string) string {
	parts := make([]string, 0, 2)
	if b := Base(); b != "" {
		parts = append(parts, b)
	}
	if r := load(role); r != "" {
		parts = append(parts, r)
	}
	return strings.Join(parts, sep)
}

// Augment appends the base + role ruleset to an existing system prompt.
func Augment(prompt, role string) string {
	s := For(role)
	switch {
	case s == "":
		return prompt
	case prompt == "":
		return s
	default:
		return prompt + sep + s
	}
}
