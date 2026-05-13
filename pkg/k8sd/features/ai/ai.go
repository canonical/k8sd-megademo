package ai

import (
	"context"
	"fmt"

	"github.com/canonical/k8sd/pkg/client/helm"
	"github.com/canonical/k8sd/pkg/k8sd/types"
	"github.com/canonical/k8sd/pkg/snap"
)

const (
	enabledMsg          = "enabled"
	disabledMsg         = "disabled"
	deleteFailedMsgTmpl = "Failed to delete AI feature, the error was: %v"
	deployFailedMsgTmpl = "Failed to deploy AI feature, the error was: %v"
)

func ApplyAI(ctx context.Context, snap snap.Snap, cfg types.AI, _ types.Annotations) (types.FeatureStatus, error) {
	m := snap.HelmClient()

	values := map[string]any{
		"provider": map[string]any{
			"model": cfg.GetProviderModel(),
			"token": cfg.GetProviderToken(),
		},
		"gateway": map[string]any{
			"enabled": true,
			"image": map[string]any{
				"repository": imageRepo,
				"tag":        imageTag,
			},
		},
		"localInference": map[string]any{
			"enabled": cfg.GetLocalInferenceEnabled(),
			"ollama": map[string]any{
				"image": map[string]any{
					"repository": ollamaRepo,
					"tag":        ollamaTag,
				},
				"models": cfg.GetLocalInferenceModels(),
				"persistence": map[string]any{
					"enabled": cfg.GetLocalInferencePersistenceEnabled(),
					"size":    cfg.GetLocalInferencePersistenceSize(),
				},
			},
		},
	}

	_, err := m.Apply(ctx, Chart, helm.StatePresentOrDeleted(cfg.GetEnabled()), values)
	if err != nil {
		if cfg.GetEnabled() {
			err = fmt.Errorf("failed to install AI chart: %w", err)
			return types.FeatureStatus{
				Enabled: false,
				Version: imageTag,
				Message: fmt.Sprintf(deployFailedMsgTmpl, err),
			}, err
		} else {
			err = fmt.Errorf("failed to delete AI chart: %w", err)
			return types.FeatureStatus{
				Enabled: false,
				Version: imageTag,
				Message: fmt.Sprintf(deleteFailedMsgTmpl, err),
			}, err
		}
	}

	if cfg.GetEnabled() {
		return types.FeatureStatus{
			Enabled: true,
			Version: imageTag,
			Message: enabledMsg,
		}, nil
	}

	return types.FeatureStatus{
		Enabled: false,
		Version: imageTag,
		Message: disabledMsg,
	}, nil
}