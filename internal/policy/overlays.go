package policy

import (
	"fmt"
	"path/filepath"

	loaderutil "github.com/ncecere/agent/internal/loader"
	"github.com/ncecere/agent/pkg/policy"
)

type overlayFile struct {
	Version int           `yaml:"version"`
	Rules   []overlayRule `yaml:"rules"`
}

type overlayRule struct {
	ID       string `yaml:"id"`
	Action   string `yaml:"action"`
	Tool     string `yaml:"tool,omitempty"`
	Decision string `yaml:"decision"`
}

type OverlayDecisions struct {
	Tools   map[string]policy.DecisionKind
	Shell   *policy.DecisionKind
	Network *policy.DecisionKind
}

func LoadToolOverrides(profilePath string, overlays []string) (OverlayDecisions, error) {
	if len(overlays) == 0 {
		return OverlayDecisions{Tools: map[string]policy.DecisionKind{}}, nil
	}
	baseDir := filepath.Dir(profilePath)
	result := OverlayDecisions{Tools: make(map[string]policy.DecisionKind)}
	for _, overlay := range overlays {
		path := overlay
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, overlay)
		}
		parsed, err := loaderutil.LoadYAML[overlayFile](path)
		if err != nil {
			return OverlayDecisions{}, err
		}
		for _, rule := range parsed.Rules {
			if rule.Decision == "" {
				continue
			}
			switch rule.Action {
			case string(policy.ActionTool), string(policy.ActionShell), string(policy.ActionNet):
			default:
				continue
			}
			decision, err := parseDecision(rule.Decision)
			if err != nil {
				return OverlayDecisions{}, fmt.Errorf("overlay rule %s: %w", rule.ID, err)
			}
			switch rule.Action {
			case string(policy.ActionTool):
				if rule.Tool == "" {
					continue
				}
				result.Tools[rule.Tool] = decision
			case string(policy.ActionShell):
				result.Shell = &decision
			case string(policy.ActionNet):
				result.Network = &decision
			}
		}
	}
	return result, nil
}

func parseDecision(input string) (policy.DecisionKind, error) {
	switch policy.DecisionKind(input) {
	case policy.DecisionAllow, policy.DecisionDeny, policy.DecisionRequireApproval:
		return policy.DecisionKind(input), nil
	default:
		return "", fmt.Errorf("unsupported decision %q", input)
	}
}
