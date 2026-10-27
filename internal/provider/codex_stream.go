package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

type codexItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Arguments string `json:"arguments"`
	Phase     string `json:"phase"`
	Encrypted string `json:"encrypted_content"`
	Summary   []struct {
		Text string `json:"text"`
	} `json:"summary"`
	Content []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
}

type codexFinal struct {
	ID         string            `json:"id"`
	Model      string            `json:"model"`
	Status     string            `json:"status"`
	Output     []json.RawMessage `json:"output"`
	Incomplete struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Usage struct {
		Input        int64 `json:"input_tokens"`
		Output       int64 `json:"output_tokens"`
		Total        int64 `json:"total_tokens"`
		InputDetails struct {
			Cached  int64 `json:"cached_tokens"`
			Written int64 `json:"cache_write_tokens"`
		} `json:"input_tokens_details"`
	} `json:"usage"`
}

type codexSlot struct {
	item                 codexItem
	raw                  json.RawMessage
	done                 bool
	text, thinking, args strings.Builder
}

func parseCodexSSE(r io.Reader, idle time.Duration, ctx context.Context, onText func(string), req Request) (*Response, error) {
	sr := newStreamReader(ctx, r, idle)
	defer sr.close()
	scanner := bufio.NewScanner(io.LimitReader(sr, maxStreamBytes+1))
	scanner.Buffer(make([]byte, 64*1024), maxStreamBytes+1)
	slots := map[int]*codexSlot{}
	var terminal *codexFinal
	var data []string
	total := 0
	consume := func() error {
		if len(data) == 0 {
			return nil
		}
		encoded := strings.Join(data, "\n")
		data = nil
		if strings.TrimSpace(encoded) == "[DONE]" {
			return nil
		}
		var event struct {
			Type      string          `json:"type"`
			Index     *int            `json:"output_index"`
			Item      json.RawMessage `json:"item"`
			Delta     string          `json:"delta"`
			Arguments string          `json:"arguments"`
			Response  *codexFinal     `json:"response"`
		}
		if err := json.Unmarshal([]byte(encoded), &event); err != nil {
			return fmt.Errorf("malformed Codex SSE JSON")
		}
		if terminal != nil {
			return fmt.Errorf("Codex data arrived after completion")
		}
		switch event.Type {
		case "error", "response.failed":
			return fmt.Errorf("Codex backend reported a failed response")
		case "response.completed", "response.done", "response.incomplete":
			if event.Response == nil {
				return fmt.Errorf("Codex terminal event lacks a response")
			}
			terminal = event.Response
			if event.Type == "response.incomplete" && terminal.Status == "" {
				terminal.Status = "incomplete"
			}
		case "response.output_item.added", "response.output_item.done":
			if event.Index == nil || *event.Index < 0 || *event.Index >= 4096 {
				return fmt.Errorf("invalid Codex output index")
			}
			var item codexItem
			if err := json.Unmarshal(event.Item, &item); err != nil || item.ID == "" {
				return fmt.Errorf("invalid Codex output item")
			}
			switch item.Type {
			case "message", "reasoning", "function_call":
			default:
				return fmt.Errorf("unsupported Codex output item %q", item.Type)
			}
			slot := slots[*event.Index]
			if slot == nil {
				slot = &codexSlot{item: item}
				slots[*event.Index] = slot
			} else if slot.done || slot.item.Type != item.Type || slot.item.ID != item.ID || event.Type == "response.output_item.added" {
				return fmt.Errorf("duplicate or mismatched Codex output item")
			}
			slot.item, slot.raw = item, append(json.RawMessage(nil), event.Item...)
			if event.Type == "response.output_item.done" {
				slot.done = true
			} else {
				slot.args.WriteString(item.Arguments)
			}
		case "response.output_text.delta", "response.refusal.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta", "response.reasoning_summary_part.done", "response.function_call_arguments.delta", "response.function_call_arguments.done":
			if event.Index == nil || slots[*event.Index] == nil || slots[*event.Index].done {
				return fmt.Errorf("Codex delta has no active output item")
			}
			slot := slots[*event.Index]
			switch event.Type {
			case "response.output_text.delta", "response.refusal.delta":
				if slot.item.Type != "message" {
					return fmt.Errorf("Codex text delta has wrong item type")
				}
				slot.text.WriteString(event.Delta)
				if onText != nil {
					onText(event.Delta)
				}
			case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
				if slot.item.Type != "reasoning" {
					return fmt.Errorf("Codex reasoning delta has wrong item type")
				}
				slot.thinking.WriteString(event.Delta)
			case "response.reasoning_summary_part.done":
				slot.thinking.WriteString("\n\n")
			case "response.function_call_arguments.delta":
				if slot.item.Type != "function_call" {
					return fmt.Errorf("Codex argument delta has wrong item type")
				}
				slot.args.WriteString(event.Delta)
			case "response.function_call_arguments.done":
				if slot.item.Type != "function_call" {
					return fmt.Errorf("Codex arguments have wrong item type")
				}
				slot.args.Reset()
				slot.args.WriteString(event.Arguments)
			}
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		total += len(line) + 1
		if total > maxStreamBytes {
			return nil, fmt.Errorf("Codex stream exceeds size limit")
		}
		if line == "" {
			if err := consume(); err != nil {
				return nil, err
			}
			if terminal != nil {
				break
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := consume(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if terminal == nil {
		return nil, fmt.Errorf("Codex stream ended before a terminal response event")
	}
	if terminal.Status != "completed" && terminal.Status != "incomplete" {
		return nil, fmt.Errorf("Codex response did not complete successfully")
	}
	result := &Response{ResponseID: terminal.ID, Model: terminal.Model, StopReason: "stop"}
	if result.Model == "" {
		result.Model = req.Model.ID
	}
	if terminal.Status == "incomplete" {
		if terminal.Incomplete.Reason != "max_output_tokens" {
			return nil, fmt.Errorf("Codex response was incomplete")
		}
		result.StopReason = "length"
	}
	// Full terminal items can backfill encrypted reasoning omitted from .done.
	finalItems := map[string]json.RawMessage{}
	for _, raw := range terminal.Output {
		var item codexItem
		if json.Unmarshal(raw, &item) != nil {
			return nil, fmt.Errorf("invalid Codex terminal output")
		}
		finalItems[item.ID] = raw
	}
	indices := make([]int, 0, len(slots))
	for i := range slots {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	seenIDs, seenCalls := map[string]bool{}, map[string]bool{}
	for _, i := range indices {
		slot := slots[i]
		if !slot.done {
			return nil, fmt.Errorf("Codex response contains an unfinished output item")
		}
		item := slot.item
		if seenIDs[item.ID] {
			return nil, fmt.Errorf("duplicate Codex output item ID")
		}
		seenIDs[item.ID] = true
		var block Block
		switch item.Type {
		case "reasoning":
			if raw := finalItems[item.ID]; len(raw) > 0 {
				var final codexItem
				if json.Unmarshal(raw, &final) == nil && final.Type == "reasoning" && final.Encrypted != "" {
					item, slot.raw = final, raw
				}
			}
			var parts []string
			for _, s := range item.Summary {
				parts = append(parts, s.Text)
			}
			thinking := strings.Join(parts, "\n\n")
			if thinking == "" {
				thinking = slot.thinking.String()
			}
			block = Block{Type: "thinking", Thinking: thinking, ThinkingSignature: string(slot.raw)}
			result.Thinking += thinking
		case "message":
			var text strings.Builder
			for _, c := range item.Content {
				switch c.Type {
				case "output_text":
					text.WriteString(c.Text)
				case "refusal":
					text.WriteString(c.Refusal)
				default:
					return nil, fmt.Errorf("unsupported Codex message content")
				}
			}
			content := text.String()
			if slot.text.Len() > 0 && slot.text.String() != content {
				return nil, fmt.Errorf("Codex completed text differs from its deltas")
			}
			if slot.text.Len() == 0 && content != "" && onText != nil {
				onText(content)
			}
			sig := map[string]any{"v": 1, "id": item.ID}
			if item.Phase != "" {
				sig["phase"] = item.Phase
			}
			encoded, _ := json.Marshal(sig)
			block = Block{Type: "text", Text: content, TextSignature: string(encoded)}
			result.Text += content
		case "function_call":
			if item.CallID == "" || item.Name == "" || strings.Contains(item.CallID, "|") || strings.Contains(item.ID, "|") || seenCalls[item.CallID] {
				return nil, fmt.Errorf("invalid or duplicate completed Codex tool call")
			}
			seenCalls[item.CallID] = true
			args := item.Arguments
			if args == "" {
				args = slot.args.String()
			}
			if !json.Valid([]byte(args)) {
				return nil, fmt.Errorf("invalid completed Codex tool arguments")
			}
			var arguments map[string]any
			decoder := json.NewDecoder(strings.NewReader(args))
			decoder.UseNumber()
			if decoder.Decode(&arguments) != nil || arguments == nil {
				return nil, fmt.Errorf("Codex tool arguments must be an object")
			}
			block = Block{Type: "toolCall", ID: item.CallID + "|" + item.ID, Name: item.Name, Arguments: arguments, Namespace: item.Namespace}
			result.ToolCalls = append(result.ToolCalls, block)
		}
		result.Content = append(result.Content, block)
	}
	if len(result.Content) == 0 {
		return nil, fmt.Errorf("Codex response contained no supported output")
	}
	if len(result.ToolCalls) > 0 && result.StopReason == "stop" {
		result.StopReason = "toolUse"
	}
	usage := terminal.Usage
	result.Usage = Usage{Input: max(0, usage.Input-usage.InputDetails.Cached-usage.InputDetails.Written), Output: usage.Output,
		CacheRead: usage.InputDetails.Cached, CacheWrite: usage.InputDetails.Written, TotalTokens: usage.Total}
	if result.Usage.TotalTokens == 0 {
		result.Usage.TotalTokens = result.Usage.Input + result.Usage.Output + result.Usage.CacheRead + result.Usage.CacheWrite
	}
	return result, nil
}
