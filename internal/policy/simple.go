package policy

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ncecere/agent/pkg/policy"
	"github.com/ncecere/agent/pkg/workspace"
)

type Engine struct {
	Workspace      workspace.Workspace
	ReadOnly       bool
	AllowNet       bool
	SensitiveTools map[string]policy.RiskLevel
	ToolOverrides  map[string]policy.DecisionKind
	ShellOverride  *policy.DecisionKind
	NetOverride    *policy.DecisionKind
}

func (e Engine) Check(_ context.Context, req policy.CheckRequest) (policy.Decision, error) {
	switch req.Action {
	case policy.ActionRead:
		return e.checkWorkspace(req.Path, policy.DecisionAllow, "read allowed in workspace")
	case policy.ActionWrite, policy.ActionEdit:
		if e.ReadOnly {
			return policy.Decision{Kind: policy.DecisionDeny, Reason: "profile is read-only", Risk: policy.RiskHigh}, nil
		}
		return e.checkWorkspace(req.Path, policy.DecisionAllow, "write allowed in workspace")
	case policy.ActionShell:
		if e.ShellOverride != nil {
			return policy.Decision{Kind: *e.ShellOverride, Reason: "shell matched policy override", Risk: policy.RiskHigh}, nil
		}
		return policy.Decision{Kind: policy.DecisionRequireApproval, Reason: "shell commands require approval", Risk: policy.RiskHigh}, nil
	case policy.ActionNet:
		if e.NetOverride != nil {
			return policy.Decision{Kind: *e.NetOverride, Reason: "network matched policy override", Risk: req.Risk}, nil
		}
		if !e.AllowNet {
			return policy.Decision{Kind: policy.DecisionDeny, Reason: "network access is disabled", Risk: policy.RiskHigh}, nil
		}
		return policy.Decision{Kind: policy.DecisionAllow, Reason: "network allowed", Risk: req.Risk}, nil
	case policy.ActionTool:
		if decision, ok := e.ToolOverrides[req.ToolID]; ok {
			return policy.Decision{Kind: decision, Reason: fmt.Sprintf("tool %s matched policy override", req.ToolID), Risk: req.Risk}, nil
		}
		if risk, ok := e.SensitiveTools[req.ToolID]; ok {
			return policy.Decision{Kind: policy.DecisionRequireApproval, Reason: fmt.Sprintf("tool %s requires approval", req.ToolID), Risk: risk}, nil
		}
		return policy.Decision{Kind: policy.DecisionAllow, Reason: "tool allowed", Risk: req.Risk}, nil
	default:
		return policy.Decision{Kind: policy.DecisionAllow, Reason: "default allow", Risk: req.Risk}, nil
	}
}

func (e Engine) checkWorkspace(path string, allowed policy.DecisionKind, reason string) (policy.Decision, error) {
	if path == "" {
		return policy.Decision{Kind: policy.DecisionDeny, Reason: "path is required", Risk: policy.RiskHigh}, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return policy.Decision{}, err
	}
	if !e.Workspace.Contains(abs) {
		return policy.Decision{Kind: policy.DecisionDeny, Reason: fmt.Sprintf("path %s is outside workspace", abs), Risk: policy.RiskHigh}, nil
	}
	return policy.Decision{Kind: allowed, Reason: reason, Risk: policy.RiskLow}, nil
}

func IsReadOnly(writeScope string, enabledTools []string) bool {
	if strings.EqualFold(writeScope, "read-only") {
		return true
	}
	for _, toolID := range enabledTools {
		if toolID == "core/write" || toolID == "core/edit" || toolID == "core/bash" {
			return false
		}
	}
	return true
}
