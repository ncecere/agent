package core

import (
	"context"
	"fmt"
	"os"

	"github.com/ncecere/agent/pkg/tool"
)

type ReadTool struct{}

func (ReadTool) Definition() tool.Definition {
	return tool.Definition{ID: "core/read", Description: "Read a file from the local workspace"}
}

func (ReadTool) Run(_ context.Context, call tool.Call) (tool.Result, error) {
	path, err := argString(call.Arguments, "path")
	if err != nil {
		return tool.Result{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{ToolID: call.ToolID, Output: string(data), Data: map[string]any{"path": path, "bytes": len(data)}}, nil
}

func argString(args map[string]any, key string) (string, error) {
	v, ok := args[key]
	if !ok {
		return "", fmt.Errorf("missing argument %q", key)
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return "", fmt.Errorf("argument %q must be a non-empty string", key)
	}
	return s, nil
}
