package agent

import (
	"context"
	"testing"

	"psi/internal/config"
	loopctl "psi/internal/loop"
	"psi/internal/provider"
	"psi/internal/tools"
)

// A provider may return a tool it was not offered. An approval in the same
// response as a completion claim must not replace the separate check request.
func TestLoopApprovalRequiresAnAdvertisedCheckTurn(t *testing.T) {
	store, _, fake, runner, _ := testSetup(t)
	controller := loopctl.NewController(store)
	if _, err := controller.Start("goal ship"); err != nil {
		t.Fatal(err)
	}
	fake.Responses = []*provider.Response{
		batchResponse(
			provider.Block{Type: "toolCall", ID: "claim", Name: loopctl.DoneToolName, Arguments: map[string]any{"reason": "ready"}},
			provider.Block{Type: "toolCall", ID: "premature", Name: loopctl.ApproveToolName, Arguments: map[string]any{"verdict": "reached", "reason": "ready"}},
		),
		batchResponse(provider.Block{Type: "toolCall", ID: "checked", Name: loopctl.ApproveToolName, Arguments: map[string]any{"verdict": "reached", "reason": "verified"}}),
		{Text: "finished", StopReason: "stop"},
	}
	approvals := 0
	cfg := Config{
		Model: modelMeta(),
		DynamicConfig: func() (string, []config.PromptSection, []provider.ToolDef) {
			if def := loopctl.ToolDefinition(controller.Snapshot()); def != nil {
				return "", nil, []provider.ToolDef{*def}
			}
			return "", nil, nil
		},
		InternalTools: map[string]InternalToolHandler{
			loopctl.DoneToolName: func(_ context.Context, inv tools.Invocation) tools.Result {
				result := controller.HandleDone(inv.Arguments)
				return tools.Result{IsError: result.Error, Content: []tools.Block{{Type: "text", Text: result.Text}}}
			},
			loopctl.ApproveToolName: func(_ context.Context, inv tools.Invocation) tools.Result {
				approvals++
				result := controller.HandleApprove(inv.Arguments)
				return tools.Result{IsError: result.Error, Content: []tools.Block{{Type: "text", Text: result.Text}}}
			},
		},
		AfterToolCall: func(_ context.Context, inv tools.Invocation, result tools.Result) error {
			if inv.Name == loopctl.DoneToolName && !result.IsError {
				if prompt, ok := controller.EvaluationPrompt(); ok {
					_, err := store.AppendCustomMessage(loopctl.EvaluationMessageType, prompt, false, nil)
					return err
				}
			}
			return nil
		},
	}
	if err := runner.Submit(context.Background(), "start", cfg); err != nil {
		t.Fatal(err)
	}
	requests := fake.Requests()
	if len(requests) != 3 || len(requests[1].Tools) != 1 || requests[1].Tools[0].Name != loopctl.ApproveToolName {
		t.Fatalf("confirmation request not made: %+v", requests)
	}
	if approvals != 1 || controller.Snapshot().ReasonDone != "verified" {
		t.Fatalf("approvals=%d state=%+v", approvals, controller.Snapshot())
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range path {
		if entry.Message != nil && entry.Message.Message.ToolResult != nil && entry.Message.Message.ToolResult.ToolCallID == "premature" {
			if !entry.Message.Message.ToolResult.IsError {
				t.Fatal("premature approval was accepted")
			}
			return
		}
	}
	t.Fatal("premature approval result missing")
}
