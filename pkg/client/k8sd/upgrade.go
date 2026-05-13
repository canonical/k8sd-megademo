package k8sd

import (
	"context"
	"fmt"

	"github.com/canonical/k8sd/pkg/client/litellm"
	"github.com/canonical/k8sd/pkg/upgrade"
)

type UpgradeCheckRequest struct {
	FromVersion string `json:"from-version" yaml:"from-version"`
	ToVersion   string `json:"to-version" yaml:"to-version"`
}

type UpgradeWarning struct {
	Severity  string `json:"severity" yaml:"severity"`
	Component string `json:"component" yaml:"component"`
	Message   string `json:"message" yaml:"message"`
}

type UpgradeComponentResult struct {
	Name         string           `json:"name" yaml:"name"`
	FromVersion  string           `json:"from_version" yaml:"from_version"`
	ToVersion    string           `json:"to_version" yaml:"to_version"`
	RepoURL      string           `json:"repo_url" yaml:"repo_url"`
	Verdict      string           `json:"verdict" yaml:"verdict"`
	Warnings     []UpgradeWarning `json:"warnings" yaml:"warnings"`
	Remediations []string         `json:"remediations" yaml:"remediations"`
}

type UpgradeCheckResponse struct {
	FromVersion string                   `json:"from_version" yaml:"from_version"`
	ToVersion   string                   `json:"to_version" yaml:"to_version"`
	Verdict     string                   `json:"verdict" yaml:"verdict"`
	Components  []UpgradeComponentResult `json:"components" yaml:"components"`
	Summary     string                   `json:"summary" yaml:"summary"`
}

// UpgradeCheck performs an AI-assisted upgrade compatibility check by fetching
// release notes from upstream component repositories and analyzing them via the
// LiteLLM gateway deployed by the ck-ai chart.
//
// The overall flow is:
//  1. Map the requested from/to Kubernetes versions to per-component versions.
//  2. For each component with a version change, fetch the GitHub release notes.
//  3. Send the release notes to the LiteLLM gateway for safety analysis.
//  4. Aggregate per-component verdicts into an overall upgrade verdict.
func (c *k8sd) UpgradeCheck(ctx context.Context, request UpgradeCheckRequest) (UpgradeCheckResponse, error) {
	// Build component version maps from the requested versions.
	// For the Kubernetes component, we use the requested versions directly.
	// Other components use the same version for from/to as a baseline;
	// in a full implementation these would be resolved from snap metadata.
	fromVersions := buildComponentVersions(request.FromVersion)
	toVersions := buildComponentVersions(request.ToVersion)

	// Create the LiteLLM client pointing to the in-cluster AI gateway.
	llmClient := litellm.New()

	// Create the verdict analyzer and run the analysis.
	analyzer := upgrade.NewVerdictAnalyzer(llmClient)

	results, err := analyzer.AnalyzeAll(ctx, fromVersions, toVersions)
	if err != nil {
		return UpgradeCheckResponse{}, fmt.Errorf("failed to analyze upgrade: %w", err)
	}

	// Convert upgrade.ComponentResult to UpgradeComponentResult.
	components := make([]UpgradeComponentResult, 0, len(results))
	for _, r := range results {
		warnings := make([]UpgradeWarning, 0, len(r.Warnings))
		for _, w := range r.Warnings {
			warnings = append(warnings, UpgradeWarning{
				Severity:  w.Severity,
				Component: w.Component,
				Message:   w.Message,
			})
		}
		components = append(components, UpgradeComponentResult{
			Name:         r.Name,
			FromVersion:  r.FromVersion,
			ToVersion:    r.ToVersion,
			RepoURL:      r.RepoURL,
			Verdict:      r.Verdict,
			Warnings:     warnings,
			Remediations: r.Remediations,
		})
	}

	overallVerdict := upgrade.AggregateVerdict(results)

	summary := analyzer.GenerateSummary(ctx, results, overallVerdict)

	return UpgradeCheckResponse{
		FromVersion: request.FromVersion,
		ToVersion:   request.ToVersion,
		Verdict:     overallVerdict,
		Components:  components,
		Summary:     summary,
	}, nil
}

// buildComponentVersions creates a component-to-version map based on the
// Kubernetes version. The Kubernetes version is used directly; other components
// are looked up from known version associations.
//
// NOTE: In a production implementation, these mappings would come from the snap
// metadata or a version manifest file. For now, we pass the Kubernetes version
// to the kubernetes component and mark other components as needing analysis
// only when their versions differ between from/to.
func buildComponentVersions(k8sVersion string) map[string]string {
	return map[string]string{
		"kubernetes": k8sVersion,
	}
}