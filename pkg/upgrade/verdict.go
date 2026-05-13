// Package upgrade provides AI-assisted upgrade compatibility checking for
// Canonical Kubernetes components using the LiteLLM gateway.
package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/canonical/k8sd/pkg/client/litellm"
)

// Component defines a tracked upstream component with its GitHub repository.
type Component struct {
	Name    string
	RepoURL string
}

// Warning represents a single upgrade warning for a component.
type Warning struct {
	Severity  string `json:"severity" yaml:"severity"`
	Component string `json:"component" yaml:"component"`
	Message   string `json:"message" yaml:"message"`
}

// ComponentResult holds the verdict for a single component upgrade.
type ComponentResult struct {
	Name         string    `json:"name" yaml:"name"`
	FromVersion  string    `json:"from_version" yaml:"from_version"`
	ToVersion    string    `json:"to_version" yaml:"to_version"`
	RepoURL      string    `json:"repo_url" yaml:"repo_url"`
	Verdict      string    `json:"verdict" yaml:"verdict"`
	Warnings     []Warning `json:"warnings" yaml:"warnings"`
	Remediations []string  `json:"remediations" yaml:"remediations"`
}

// CheckResult holds the overall upgrade check result.
type CheckResult struct {
	FromVersion string            `json:"from_version" yaml:"from_version"`
	ToVersion   string            `json:"to_version" yaml:"to_version"`
	Verdict     string            `json:"verdict" yaml:"verdict"`
	Components  []ComponentResult `json:"components" yaml:"components"`
	Summary     string            `json:"summary" yaml:"summary"`
}

// KnownComponents is the list of components tracked by Canonical Kubernetes.
var KnownComponents = []Component{
	{Name: "kubernetes", RepoURL: "https://github.com/kubernetes/kubernetes"},
	{Name: "containerd", RepoURL: "https://github.com/containerd/containerd"},
	{Name: "runc", RepoURL: "https://github.com/opencontainers/runc"},
	{Name: "cni", RepoURL: "https://github.com/containernetworking/plugins"},
	{Name: "etcd", RepoURL: "https://github.com/etcd-io/etcd"},
	{Name: "helm", RepoURL: "https://github.com/helm/helm"},
}

// VerdictAnalyzer uses the LiteLLM gateway to analyze release notes and produce
// upgrade verdicts for each component.
type VerdictAnalyzer struct {
	llm        *litellm.Client
	httpClient *http.Client
}

// NewVerdictAnalyzer creates a new VerdictAnalyzer.
func NewVerdictAnalyzer(llm *litellm.Client) *VerdictAnalyzer {
	return &VerdictAnalyzer{
		llm: llm,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// fetchReleaseNotes retrieves the release notes for a given GitHub repo and tag
// using the GitHub Releases API.
func (v *VerdictAnalyzer) fetchReleaseNotes(ctx context.Context, repoURL string, tag string) (string, error) {
	// Convert https://github.com/owner/repo to API URL.
	parts := strings.TrimPrefix(repoURL, "https://github.com/")
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/tags/%s", parts, tag)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch release notes: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("no release found for tag %s", tag)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var release struct {
		Body string `json:"body"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &release); err != nil {
		return "", fmt.Errorf("failed to parse release: %w", err)
	}

	if release.Body == "" {
		return fmt.Sprintf("Release %s (no release notes body available)", release.Name), nil
	}

	return release.Body, nil
}

// componentVerdictResponse is the expected JSON structure from the LLM analysis.
type componentVerdictResponse struct {
	Verdict      string   `json:"verdict"`
	Warnings     []string `json:"warnings"`
	Remediations []string `json:"remediations"`
	Summary      string   `json:"summary"`
}

// AnalyzeComponent fetches the release notes for a component upgrade and uses
// the LiteLLM gateway to determine the upgrade verdict.
func (v *VerdictAnalyzer) AnalyzeComponent(ctx context.Context, comp Component, fromVersion, toVersion string) (ComponentResult, error) {
	result := ComponentResult{
		Name:        comp.Name,
		FromVersion: fromVersion,
		ToVersion:   toVersion,
		RepoURL:     comp.RepoURL,
		Verdict:     "pass",
	}

	releaseNotes, err := v.fetchReleaseNotes(ctx, comp.RepoURL, toVersion)
	if err != nil {
		// If we cannot fetch release notes, warn but don't block.
		result.Verdict = "warn"
		result.Warnings = []Warning{{
			Severity:  "medium",
			Component: comp.Name,
			Message:   fmt.Sprintf("Could not fetch release notes for %s %s: %v", comp.Name, toVersion, err),
		}}
		return result, nil
	}

	prompt := buildAnalysisPrompt(comp.Name, fromVersion, toVersion, releaseNotes)

	messages := []litellm.Message{
		{
			Role: "system",
			Content: `You are an upgrade safety analyst for Canonical Kubernetes. Your job is to analyze release notes of upstream components and determine whether upgrading is safe.

You must respond with a JSON object (no markdown fences) with these fields:
- "verdict": one of "pass", "warn", or "blocked"
  - "pass": the upgrade is safe with no concerns
  - "warn": the upgrade is likely safe but has notable changes (deprecations, config changes, new defaults)
  - "blocked": the upgrade has breaking changes, removed APIs, or known critical issues
- "warnings": array of short warning strings describing concerns (empty if verdict is "pass")
- "remediations": array of actionable steps to address warnings (empty if verdict is "pass")
- "summary": a one-sentence summary of the upgrade impact`,
		},
		{
			Role:    "user",
			Content: prompt,
		},
	}

	response, err := v.llm.ChatCompletion(ctx, messages)
	if err != nil {
		// LLM unavailable: default to warn so we don't silently pass.
		result.Verdict = "warn"
		result.Warnings = []Warning{{
			Severity:  "low",
			Component: comp.Name,
			Message:   fmt.Sprintf("AI analysis unavailable for %s: %v. Manual review recommended.", comp.Name, err),
		}}
		return result, nil
	}

	var parsed componentVerdictResponse
	// Strip potential markdown code fences from LLM output.
	cleaned := strings.TrimSpace(response)
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	cleaned = strings.TrimSpace(cleaned)

	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		result.Verdict = "warn"
		result.Warnings = []Warning{{
			Severity:  "low",
			Component: comp.Name,
			Message:   fmt.Sprintf("Could not parse AI verdict for %s, raw response: %s", comp.Name, truncate(response, 200)),
		}}
		return result, nil
	}

	// Validate verdict value.
	switch parsed.Verdict {
	case "pass", "warn", "blocked":
		result.Verdict = parsed.Verdict
	default:
		result.Verdict = "warn"
	}

	for _, w := range parsed.Warnings {
		result.Warnings = append(result.Warnings, Warning{
			Severity:  severityFromVerdict(parsed.Verdict),
			Component: comp.Name,
			Message:   w,
		})
	}
	result.Remediations = parsed.Remediations

	return result, nil
}

// AnalyzeAll runs the verdict analysis for all known components.
func (v *VerdictAnalyzer) AnalyzeAll(ctx context.Context, fromVersions, toVersions map[string]string) ([]ComponentResult, error) {
	var results []ComponentResult

	for _, comp := range KnownComponents {
		from, hasFrom := fromVersions[comp.Name]
		to, hasTo := toVersions[comp.Name]
		if !hasFrom || !hasTo {
			continue
		}
		if from == to {
			// No version change, auto-pass.
			results = append(results, ComponentResult{
				Name:        comp.Name,
				FromVersion: from,
				ToVersion:   to,
				RepoURL:     comp.RepoURL,
				Verdict:     "pass",
			})
			continue
		}

		result, err := v.AnalyzeComponent(ctx, comp, from, to)
		if err != nil {
			return nil, fmt.Errorf("failed to analyze %s: %w", comp.Name, err)
		}
		results = append(results, result)
	}

	return results, nil
}

// AggregateVerdict computes the overall verdict from component results.
// The overall verdict is the worst among all components.
func AggregateVerdict(components []ComponentResult) string {
	verdict := "pass"
	for _, c := range components {
		if c.Verdict == "blocked" {
			return "blocked"
		}
		if c.Verdict == "warn" {
			verdict = "warn"
		}
	}
	return verdict
}

// GenerateSummary uses the LLM to produce a human-friendly overall summary.
func (v *VerdictAnalyzer) GenerateSummary(ctx context.Context, components []ComponentResult, overallVerdict string) string {
	var sb strings.Builder
	for _, c := range components {
		sb.WriteString(fmt.Sprintf("- %s: %s -> %s [%s]\n", c.Name, c.FromVersion, c.ToVersion, c.Verdict))
		for _, w := range c.Warnings {
			sb.WriteString(fmt.Sprintf("  Warning: %s\n", w.Message))
		}
	}

	messages := []litellm.Message{
		{
			Role:    "system",
			Content: "You are a Kubernetes upgrade advisor. Summarize the upgrade check results in 2-3 concise sentences for a cluster administrator. Be direct about risks and required actions.",
		},
		{
			Role:    "user",
			Content: fmt.Sprintf("Overall verdict: %s\n\nComponent details:\n%s", overallVerdict, sb.String()),
		},
	}

	summary, err := v.llm.ChatCompletion(ctx, messages)
	if err != nil {
		// Fallback to a generated summary without LLM.
		return buildFallbackSummary(components, overallVerdict)
	}
	return strings.TrimSpace(summary)
}

func buildAnalysisPrompt(component, fromVersion, toVersion, releaseNotes string) string {
	return fmt.Sprintf(`Analyze the following release notes for upgrading %s from %s to %s.

Focus on:
1. Breaking changes or removed APIs
2. Deprecated features that may affect Canonical Kubernetes
3. Configuration changes or new required settings
4. Known issues or bugs that could impact cluster stability
5. Security fixes (these favor upgrading)

Release notes for %s %s:
---
%s
---

Provide your verdict as JSON.`, component, fromVersion, toVersion, component, toVersion, releaseNotes)
}

func buildFallbackSummary(components []ComponentResult, verdict string) string {
	var warnings, blocked int
	for _, c := range components {
		switch c.Verdict {
		case "warn":
			warnings++
		case "blocked":
			blocked++
		}
	}

	switch verdict {
	case "pass":
		return fmt.Sprintf("All %d components pass upgrade checks. No issues detected.", len(components))
	case "warn":
		return fmt.Sprintf("Upgrade has warnings for %d of %d components. Review the warnings before proceeding.", warnings, len(components))
	case "blocked":
		return fmt.Sprintf("Upgrade is blocked by %d component(s). %d additional warning(s). Resolve blockers before upgrading.", blocked, warnings)
	default:
		return "Upgrade check completed. Review component details."
	}
}

func severityFromVerdict(verdict string) string {
	switch verdict {
	case "blocked":
		return "high"
	case "warn":
		return "medium"
	default:
		return "low"
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
