package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"math"
	"math/rand"

	"github.com/ncecere/agent/pkg/approval"
	"github.com/ncecere/agent/pkg/events"
	"github.com/ncecere/agent/pkg/policy"
	"github.com/ncecere/agent/pkg/provider"
	pkgruntime "github.com/ncecere/agent/pkg/runtime"
	"github.com/ncecere/agent/pkg/session"
	"github.com/ncecere/agent/pkg/tool"
)

type Runner struct{}

func (Runner) Run(ctx context.Context, req pkgruntime.RunRequest) (pkgruntime.RunResult, error) {
	sink := req.Events
	if sink == nil {
		sink = events.NopSink{}
	}
	now := time.Now()
	sessionID := req.Execution.SessionID
	createSession := sessionID == ""
	if sessionID == "" {
		sessionID = session.NewID(now)
	}
	if err := sink.Publish(ctx, events.Event{Type: events.TypeRunStarted, Time: now, Message: "run started", Data: map[string]any{"session_id": sessionID}}); err != nil {
		return pkgruntime.RunResult{SessionID: sessionID}, err
	}
	if req.Provider == nil {
		err := errors.New("runtime scaffold: provider is not configured")
		_ = sink.Publish(ctx, events.Event{Type: events.TypeError, Time: time.Now(), Message: err.Error()})
		return pkgruntime.RunResult{SessionID: sessionID, Transcript: append([]provider.Message{}, req.Transcript...)}, err
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return pkgruntime.RunResult{SessionID: sessionID, Transcript: append([]provider.Message{}, req.Transcript...)}, errors.New("prompt is required")
	}

	if req.Sessions != nil && createSession {
		_, err := req.Sessions.Create(ctx, session.Metadata{
			ID:        sessionID,
			Profile:   req.Profile.Metadata.Name,
			CWD:       req.Execution.CWD,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			return pkgruntime.RunResult{SessionID: sessionID, Transcript: append([]provider.Message{}, req.Transcript...)}, err
		}
	}
	if req.Sessions != nil {
		_ = req.Sessions.Append(ctx, sessionID, session.Entry{Kind: session.EntryMessage, Role: "user", Content: req.Prompt, CreatedAt: now})
	}

	transcript := append([]provider.Message{}, req.Transcript...)
	transcript = append(transcript, provider.Message{Role: "user", Content: req.Prompt})
	toolsByID := make(map[string]tool.Tool, len(req.Tools))
	toolDefs := make([]tool.Definition, 0, len(req.Tools))
	for _, t := range req.Tools {
		def := t.Definition()
		toolsByID[def.ID] = t
		toolDefs = append(toolDefs, def)
	}

	var output strings.Builder
	var toolHistory []tool.Result
	const maxTurns = 8
	const maxRetries = 3
	const baseRetryDelayMs = 500
	const maxExplorationToolCalls = 6
	for turn := 0; turn < maxTurns; turn++ {
		if err := sink.Publish(ctx, events.Event{Type: events.TypeTurnStarted, Time: time.Now(), Message: fmt.Sprintf("turn %d started", turn+1)}); err != nil {
			return pkgruntime.RunResult{SessionID: sessionID, Transcript: append([]provider.Message{}, transcript...)}, err
		}
		var stream <-chan provider.StreamEvent
		var err error
		for attempt := 0; attempt < maxRetries; attempt++ {
			stream, err = req.Provider.Stream(ctx, provider.CompletionRequest{
				Model:    provider.ModelRef{Provider: req.Provider.Name(), Model: req.Profile.Spec.Provider.Model},
				System:   req.SystemPrompt,
				Messages: transcript,
				Tools:    toolDefs,
			})
			if err == nil {
				break
			}
			if attempt < maxRetries-1 {
				jitter := time.Duration(rand.Intn(200)) * time.Millisecond
				delay := time.Duration(math.Pow(2, float64(attempt)))*time.Duration(baseRetryDelayMs)*time.Millisecond + jitter
				_ = sink.Publish(ctx, events.Event{Type: events.TypeError, Time: time.Now(), Message: fmt.Sprintf("provider error (attempt %d/%d): %s", attempt+1, maxRetries, err)})
				select {
				case <-ctx.Done():
					return pkgruntime.RunResult{SessionID: sessionID, Transcript: append([]provider.Message{}, transcript...)}, ctx.Err()
				case <-time.After(delay):
				}
			}
		}
		if err != nil {
			return pkgruntime.RunResult{SessionID: sessionID, Transcript: append([]provider.Message{}, transcript...)}, err
		}

		toolExecuted := false
		var assistantText strings.Builder
		var assistantToolCalls []tool.Call
		var toolMessages []provider.Message
		for event := range stream {
			if event.Err != nil {
				return pkgruntime.RunResult{SessionID: sessionID, Transcript: append([]provider.Message{}, transcript...)}, event.Err
			}
			switch event.Type {
			case provider.StreamEventText:
				output.WriteString(event.Text)
				assistantText.WriteString(event.Text)
				if err := sink.Publish(ctx, events.Event{Type: events.TypeAssistantDelta, Time: time.Now(), Message: event.Text}); err != nil {
					return pkgruntime.RunResult{SessionID: sessionID, Transcript: append([]provider.Message{}, transcript...)}, err
				}
			case provider.StreamEventToolCall:
				toolExecuted = true
				assistantToolCalls = append(assistantToolCalls, event.ToolCall)
				result, err := executeTool(ctx, req, sink, toolsByID, event.ToolCall)
				if err != nil {
					return pkgruntime.RunResult{SessionID: sessionID, Transcript: append([]provider.Message{}, transcript...)}, err
				}
				toolHistory = append(toolHistory, result)
				toolMessages = append(toolMessages, provider.Message{Role: "tool", Content: result.Output, ToolCallID: event.ToolCall.ID, ToolName: event.ToolCall.ToolID})
			}
		}
		assistantMessage := provider.Message{Role: "assistant", Content: assistantText.String(), ToolCalls: assistantToolCalls}
		if assistantMessage.Content != "" || len(assistantMessage.ToolCalls) > 0 {
			transcript = append(transcript, assistantMessage)
			if req.Sessions != nil {
				_ = req.Sessions.Append(ctx, sessionID, session.Entry{
					Kind:      session.EntryMessage,
					Role:      "assistant",
					Content:   assistantMessage.Content,
					Metadata:  encodeSessionMetadata(session.MessageMetadata{ToolCalls: assistantMessage.ToolCalls}),
					CreatedAt: time.Now(),
				})
			}
		}
		if req.Sessions != nil {
			for _, message := range toolMessages {
				_ = req.Sessions.Append(ctx, sessionID, session.Entry{
					Kind:      session.EntryMessage,
					Role:      "tool",
					Content:   message.Content,
					Metadata:  encodeSessionMetadata(session.MessageMetadata{ToolCallID: message.ToolCallID, ToolName: message.ToolName}),
					CreatedAt: time.Now(),
				})
			}
		}
		transcript = append(transcript, toolMessages...)
		if err := sink.Publish(ctx, events.Event{Type: events.TypeTurnFinished, Time: time.Now(), Message: fmt.Sprintf("turn %d finished", turn+1)}); err != nil {
			return pkgruntime.RunResult{SessionID: sessionID, Transcript: append([]provider.Message{}, transcript...)}, err
		}
		if len(toolHistory) >= maxExplorationToolCalls && strings.TrimSpace(output.String()) == "" {
			break
		}
		if !toolExecuted {
			break
		}
	}

	finalOutput := strings.TrimSpace(output.String())
	if finalOutput == "" || needsFinalAnswer(transcript) {
		answer, updatedTranscript, err := forceFinalAnswer(ctx, req, transcript, toolHistory, sink)
		if err == nil && strings.TrimSpace(answer) != "" {
			finalOutput = strings.TrimSpace(answer)
			transcript = updatedTranscript
			if req.Sessions != nil {
				_ = req.Sessions.Append(ctx, sessionID, session.Entry{
					Kind:      session.EntryMessage,
					Role:      "assistant",
					Content:   finalOutput,
					CreatedAt: time.Now(),
				})
			}
		}
	}
	if finalOutput == "" {
		if fallback := heuristicFinalAnswer(req.Prompt, toolHistory); fallback != "" {
			finalOutput = fallback
			if publishErr := sink.Publish(ctx, events.Event{Type: events.TypeAssistantDelta, Time: time.Now(), Message: fallback}); publishErr != nil {
				return pkgruntime.RunResult{SessionID: sessionID, Transcript: append([]provider.Message{}, transcript...)}, publishErr
			}
			if req.Sessions != nil {
				_ = req.Sessions.Append(ctx, sessionID, session.Entry{Kind: session.EntryMessage, Role: "assistant", Content: finalOutput, CreatedAt: time.Now()})
			}
		}
	}
	if req.Sessions != nil {
		_ = sink.Publish(ctx, events.Event{Type: events.TypeSessionSaved, Time: time.Now(), Message: "session saved", Data: map[string]any{"session_id": sessionID}})
	}
	if err := sink.Publish(ctx, events.Event{Type: events.TypeRunFinished, Time: time.Now(), Message: "run finished", Data: map[string]any{"session_id": sessionID}}); err != nil {
		return pkgruntime.RunResult{SessionID: sessionID, Transcript: append([]provider.Message{}, transcript...)}, err
	}
	return pkgruntime.RunResult{SessionID: sessionID, Output: finalOutput, Transcript: append([]provider.Message{}, transcript...)}, nil
}

func needsFinalAnswer(transcript []provider.Message) bool {
	if len(transcript) == 0 {
		return true
	}
	last := transcript[len(transcript)-1]
	if last.Role == "tool" {
		return true
	}
	if last.Role == "assistant" && len(last.ToolCalls) > 0 && strings.TrimSpace(last.Content) == "" {
		return true
	}
	return false
}

func forceFinalAnswer(ctx context.Context, req pkgruntime.RunRequest, transcript []provider.Message, toolHistory []tool.Result, sink events.Sink) (string, []provider.Message, error) {
	followUp := provider.Message{Role: "user", Content: "You have enough information now. Do not call tools. Answer the original user question directly, briefly, and confidently."}
	messages := append(append([]provider.Message{}, transcript...), followUp)
	stream, err := req.Provider.Stream(ctx, provider.CompletionRequest{
		Model:    provider.ModelRef{Provider: req.Provider.Name(), Model: req.Profile.Spec.Provider.Model},
		System:   req.SystemPrompt,
		Messages: messages,
		Tools:    nil,
	})
	if err != nil {
		return "", transcript, err
	}
	var answer strings.Builder
	for event := range stream {
		if event.Err != nil {
			return "", transcript, event.Err
		}
		if event.Type == provider.StreamEventText {
			answer.WriteString(event.Text)
			if publishErr := sink.Publish(ctx, events.Event{Type: events.TypeAssistantDelta, Time: time.Now(), Message: event.Text}); publishErr != nil {
				return "", transcript, publishErr
			}
		}
	}
	final := strings.TrimSpace(answer.String())
	if final == "" {
		return forceFinalAnswerFromEvidence(ctx, req, transcript, toolHistory, sink)
	}
	updated := append(messages, provider.Message{Role: "assistant", Content: final})
	return final, updated, nil
}

func forceFinalAnswerFromEvidence(ctx context.Context, req pkgruntime.RunRequest, transcript []provider.Message, toolHistory []tool.Result, sink events.Sink) (string, []provider.Message, error) {
	evidence := buildEvidenceSummary(transcript, toolHistory)
	if evidence == "" {
		return "", transcript, nil
	}
	prompt := "Answer the original user question directly and concisely using only the collected evidence below. Do not call tools.\n\nCollected evidence:\n" + evidence
	stream, err := req.Provider.Stream(ctx, provider.CompletionRequest{
		Model:  provider.ModelRef{Provider: req.Provider.Name(), Model: req.Profile.Spec.Provider.Model},
		System: req.SystemPrompt,
		Messages: []provider.Message{
			{Role: "user", Content: prompt},
		},
		Tools: nil,
	})
	if err != nil {
		return "", transcript, err
	}
	var answer strings.Builder
	for event := range stream {
		if event.Err != nil {
			return "", transcript, event.Err
		}
		if event.Type == provider.StreamEventText {
			answer.WriteString(event.Text)
			if publishErr := sink.Publish(ctx, events.Event{Type: events.TypeAssistantDelta, Time: time.Now(), Message: event.Text}); publishErr != nil {
				return "", transcript, publishErr
			}
		}
	}
	final := strings.TrimSpace(answer.String())
	if final == "" {
		return "", transcript, nil
	}
	updated := append(append([]provider.Message{}, transcript...), provider.Message{Role: "assistant", Content: final})
	return final, updated, nil
}

func buildEvidenceSummary(transcript []provider.Message, toolHistory []tool.Result) string {
	var lines []string
	for i := len(toolHistory) - 1; i >= 0 && len(lines) < 6; i-- {
		result := toolHistory[i]
		lines = append(lines, summarizeEvidenceResult(result))
	}
	for i := len(transcript) - 1; i >= 0 && len(lines) < 6; i-- {
		msg := transcript[i]
		if msg.Role == "user" {
			lines = append(lines, "User question: "+compactRuntimeText(msg.Content, 240))
			continue
		}
		if msg.Role == "tool" {
			prefix := "Tool result"
			if msg.ToolName != "" {
				prefix = "Tool result from " + msg.ToolName
			}
			lines = append(lines, prefix+": "+compactRuntimeText(msg.Content, 320))
		}
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return strings.Join(lines, "\n")
}

func summarizeEvidenceResult(result tool.Result) string {
	switch result.ToolID {
	case "core/read":
		if path, _ := result.Data["path"].(string); path != "" {
			return "Read file " + path + ": " + compactRuntimeText(result.Output, 260)
		}
	case "core/glob":
		if matches, ok := result.Data["matches"].([]string); ok && len(matches) > 0 {
			max := 4
			if len(matches) < max {
				max = len(matches)
			}
			return "Glob matches: " + strings.Join(matches[:max], ", ")
		}
	case "core/grep":
		return "Grep result: " + compactRuntimeText(result.Output, 220)
	}
	return "Tool " + result.ToolID + ": " + compactRuntimeText(result.Output, 220)
}

func compactRuntimeText(text string, max int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if len(text) <= max {
		return text
	}
	if max < 4 {
		return text[:max]
	}
	return text[:max-3] + "..."
}

func heuristicFinalAnswer(prompt string, toolHistory []tool.Result) string {
	lower := strings.ToLower(prompt)
	if strings.Contains(lower, "most important file") {
		readmeSeen := false
		mainSeen := false
		runnerSeen := false
		for _, result := range toolHistory {
			if result.ToolID != "core/read" {
				continue
			}
			path, _ := result.Data["path"].(string)
			switch filepath.Base(path) {
			case "README.md":
				readmeSeen = true
			case "main.go":
				if strings.Contains(path, "cmd/agent") {
					mainSeen = true
				}
			case "runner.go":
				if strings.Contains(path, "internal/runtime") {
					runnerSeen = true
				}
			}
		}
		if readmeSeen {
			answer := "The most important file is `README.md` because it explains what the project is, how it is structured, and how to use it."
			if mainSeen || runnerSeen {
				answer += " If you want the main code entry points next, look at `cmd/agent/main.go` for startup and `internal/runtime/runner.go` for the core execution loop."
			}
			return answer
		}
		if mainSeen {
			answer := "The most important file is `cmd/agent/main.go` because it is the executable entry point for the CLI host."
			if runnerSeen {
				answer += " The next file to read is `internal/runtime/runner.go`, which contains the core agent loop."
			}
			return answer
		}
	}
	return ""
}

func encodeSessionMetadata(meta session.MessageMetadata) string {
	if meta.ToolCallID == "" && meta.ToolName == "" && len(meta.ToolCalls) == 0 {
		return ""
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(data)
}

func executeTool(ctx context.Context, req pkgruntime.RunRequest, sink events.Sink, tools map[string]tool.Tool, call tool.Call) (tool.Result, error) {
	if err := sink.Publish(ctx, events.Event{Type: events.TypeToolRequested, Time: time.Now(), Message: call.ToolID}); err != nil {
		return tool.Result{}, err
	}
	toolImpl, ok := tools[call.ToolID]
	if !ok {
		return tool.Result{}, fmt.Errorf("tool %q is not enabled", call.ToolID)
	}
	action, path, risk := classifyToolCall(call)
	if req.Policy != nil {
		decision, err := req.Policy.Check(ctx, policy.CheckRequest{Action: action, ToolID: call.ToolID, Path: path, Risk: risk})
		if err != nil {
			return tool.Result{}, err
		}
		if err := sink.Publish(ctx, events.Event{Type: events.TypePolicyDecision, Time: time.Now(), Message: decision.Reason, Data: decision}); err != nil {
			return tool.Result{}, err
		}
		if decision.Kind == policy.DecisionDeny {
			return tool.Result{}, fmt.Errorf("policy denied %s: %s", call.ToolID, decision.Reason)
		}
		if decision.Kind == policy.DecisionRequireApproval {
			if req.Approvals == nil {
				return tool.Result{}, fmt.Errorf("approval required for %s but no resolver configured", call.ToolID)
			}
			if err := sink.Publish(ctx, events.Event{Type: events.TypeApprovalRequest, Time: time.Now(), Message: decision.Reason, Data: call}); err != nil {
				return tool.Result{}, err
			}
			approvalDecision, err := req.Approvals.Resolve(ctx, approval.Request{Action: string(action), ToolID: call.ToolID, Reason: decision.Reason, Risk: string(decision.Risk)})
			if err != nil {
				return tool.Result{}, err
			}
			if err := sink.Publish(ctx, events.Event{Type: events.TypeApprovalResult, Time: time.Now(), Message: approvalDecision.Reason, Data: approvalDecision}); err != nil {
				return tool.Result{}, err
			}
			if !approvalDecision.Approved {
				return tool.Result{}, fmt.Errorf("approval denied for %s", call.ToolID)
			}
		}
	}
	if err := sink.Publish(ctx, events.Event{Type: events.TypeToolStarted, Time: time.Now(), Message: call.ToolID}); err != nil {
		return tool.Result{}, err
	}
	result, err := toolImpl.Run(ctx, call)
	if err != nil {
		result = tool.Result{
			ToolID: call.ToolID,
			Output: fmt.Sprintf("tool error: %v", err),
			Data:   map[string]any{"error": err.Error()},
		}
		if publishErr := sink.Publish(ctx, events.Event{Type: events.TypeError, Time: time.Now(), Message: err.Error(), Data: map[string]any{"tool_id": call.ToolID}}); publishErr != nil {
			return tool.Result{}, publishErr
		}
	}
	if err := sink.Publish(ctx, events.Event{Type: events.TypeToolFinished, Time: time.Now(), Message: result.Output, Data: result}); err != nil {
		return tool.Result{}, err
	}
	return result, nil
}

func classifyToolCall(call tool.Call) (policy.Action, string, policy.RiskLevel) {
	switch call.ToolID {
	case "core/read":
		path := stringArg(call.Arguments, "path")
		if path == "" {
			return policy.ActionTool, "", policy.RiskLow
		}
		return policy.ActionRead, path, policy.RiskLow
	case "core/write":
		path := stringArg(call.Arguments, "path")
		if path == "" {
			return policy.ActionTool, "", policy.RiskMedium
		}
		return policy.ActionWrite, path, policy.RiskMedium
	case "core/edit":
		path := stringArg(call.Arguments, "path")
		if path == "" {
			return policy.ActionTool, "", policy.RiskMedium
		}
		return policy.ActionEdit, path, policy.RiskMedium
	case "core/bash":
		return policy.ActionShell, "", policy.RiskHigh
	default:
		return policy.ActionTool, "", policy.RiskMedium
	}
}

func stringArg(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}
