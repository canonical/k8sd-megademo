package ai

import (
	"path/filepath"

	"github.com/canonical/k8sd/pkg/client/helm"
)

var (
	Chart = helm.InstallableChart{
		Name:         "ck-ai",
		Namespace:    "kube-system",
		ManifestPath: filepath.Join("charts", "ck-ai-0.1.0.tgz"),
	}

	imageRepo    = "ghcr.io/berriai/litellm"
	imageTag     = "main-stable"
	ollamaRepo   = "ollama/ollama"
	ollamaTag    = "latest"
)