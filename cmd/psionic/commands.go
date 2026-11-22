package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hypernewbie/psi/internal/agent"
	"github.com/hypernewbie/psi/internal/provider"
)

// commandText recognizes the small native command set. Mention and whitespace
// normalization happens in the transport before this check. A command that
// takes an argument (/model) is recognized with the argument attached.
func commandText(text string) (string, bool) {
	command, _, _ := commandWithArg(text)
	return command, command != ""
}

// commandWithArg splits a recognized command and its single remaining
// argument. Unrecognized or malformed input returns "", false.
func commandWithArg(text string) (string, string, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return "", "", false
	}
	switch strings.ToLower(fields[0]) {
	case "/new", "/reset", "/stop", "/status", "/compact":
		if len(fields) > 1 {
			return "", "", false
		}
		return strings.ToLower(fields[0]), "", true
	case "/model":
		if len(fields) > 2 {
			return "", "", false
		}
		return "/model", strings.Join(fields[1:], " "), true
	default:
		return "", "", false
	}
}

// handleCommand reports whether the turn was a command. Only an explicit
// owner may reset, inspect, compact, or switch the model of a conversation.
func (c *conversation) handleCommand(turn sourceTurn, command, arg string) bool {
	if !c.rt.policy.isOwner(turn.message.AuthorID) {
		return false
	}
	switch command {
	case "/stop":
		stopped := c.stop()
		if turn.noAck {
			return true
		}
		if stopped {
			c.replyCommand(turn, "✅ Stopped.")
		} else {
			c.replyCommand(turn, "Nothing to stop.")
		}
		return true
	case "/new", "/reset":
		c.mu.Lock()
		if c.running || c.runStop != nil {
			c.mu.Unlock()
			if !turn.noAck {
				c.replyCommand(turn, "Busy; stop the active run first.")
			}
			return true
		}
		c.retireLocked()
		c.mu.Unlock()
		if err := c.rt.state.deleteRoute(c.key); err != nil {
			c.rt.logger.Printf("conversation %s: reset failed (%T)", shortKey(c.key), err)
			c.replyCommand(turn, "Reset failed.")
			return true
		}
		if !turn.noAck {
			if command == "/reset" {
				c.replyCommand(turn, "✅ Session reset.")
			} else {
				c.replyCommand(turn, "✅ New session started.")
			}
		}
		return true
	case "/status":
		c.mu.Lock()
		model := c.modelRef()
		c.mu.Unlock()
		c.replyCommand(turn, fmt.Sprintf("Model: %s\nStatus: %s", model, c.status()))
		return true
	case "/model":
		c.handleModel(turn, arg)
		return true
	case "/compact":
		c.handleCompact(turn)
		return true
	default:
		return false
	}
}

// replyCommand sends a command acknowledgement outside the normal reply mode.
func (c *conversation) replyCommand(turn sourceTurn, text string) {
	if err := c.rt.deliver(turn, text); err != nil {
		c.rt.logger.Printf("conversation %s: command reply failed (%T)", shortKey(c.key), err)
	}
}

// handleModel lists the selectable models for the current provider or switches
// the per-route model. Selection is validated against Psi's own catalogue and
// credentials; the process default in Psi's settings is never rewritten.
func (c *conversation) handleModel(turn sourceTurn, arg string) {
	if arg == "" {
		c.replyCommand(turn, c.modelList())
		return
	}
	model, catalogue, providerID, err := resolveModel(c.rt.models, arg)
	if err == nil {
		var client provider.Provider
		client, err = buildProvider(c.rt.models, c.rt.agentDir, providerID, model, catalogue, 1, "check")
		if client != nil {
			client.CloseIdle()
		}
	}
	if err != nil {
		c.replyCommand(turn, "That model is unavailable here; send /model with no argument to list models.")
		return
	}
	ref := providerID + "/" + model.ID
	c.mu.Lock()
	if c.running || c.runStop != nil {
		c.mu.Unlock()
		if !turn.noAck {
			c.replyCommand(turn, "Busy; stop the active run first.")
		}
		return
	}
	state, _, err := c.rt.state.route(c.key)
	if err == nil {
		state.Model = ref
		if c.store != nil {
			if _, aerr := c.store.AppendModelChange(providerID, model.ID); aerr != nil {
				err = aerr
			} else {
				state.SessionID = c.store.Header().ID
			}
		}
	}
	if err == nil {
		err = c.rt.state.saveRoute(c.key, state)
	}
	if err == nil {
		c.modelOverride = ref
		// Retire the open session and clients so the next turn rebuilds on
		// the new model. The route state keeps the session linkage.
		c.retireLocked()
		c.mu.Unlock()
		if !turn.noAck {
			c.replyCommand(turn, "✅ Model set to "+ref+".")
		}
		return
	}
	c.mu.Unlock()
	c.rt.logger.Printf("conversation %s: model change failed (%T)", shortKey(c.key), err)
	if !turn.noAck {
		c.replyCommand(turn, "Model change failed.")
	}
}

// modelList reports the models Psi can resolve for the route's current
// provider. Output is bounded and contains only provider and model IDs.
func (c *conversation) modelList() string {
	c.mu.Lock()
	ref := c.modelRef()
	c.mu.Unlock()
	providerID, currentID, err := splitModelRef(ref)
	if err != nil || providerID == "" {
		return "No model is selected."
	}
	models := c.rt.models.LocalModels(providerID)
	if len(models) == 0 {
		return "No selectable models for this provider."
	}
	const maxListed = 20
	shown, more := models, 0
	if len(shown) > maxListed {
		more = len(shown) - maxListed
		shown = shown[:maxListed]
	}
	lines := []string{"Models (" + providerID + "):"}
	for _, m := range shown {
		line := "- " + providerID + "/" + m.ID
		if m.ID == currentID {
			line += "  (current)"
		}
		lines = append(lines, line)
	}
	if more > 0 {
		lines = append(lines, fmt.Sprintf("… and %d more", more))
	}
	lines = append(lines, "Send /model provider/model to switch.")
	return strings.Join(lines, "\n")
}

// handleCompact runs Psi's compaction on this conversation. It is serialized
// with ordinary turns through the caller and reports fixed safe categories.
func (c *conversation) handleCompact(turn sourceTurn) {
	c.mu.Lock()
	if c.running || c.runStop != nil {
		c.mu.Unlock()
		if !turn.noAck {
			c.replyCommand(turn, "Busy; stop the active run first.")
		}
		return
	}
	c.mu.Unlock()
	if err := c.prepare(turn.message); err != nil {
		c.rt.logger.Printf("conversation %s: compact unavailable (%T)", shortKey(c.key), err)
		if !turn.noAck {
			c.replyCommand(turn, "This conversation is temporarily unavailable.")
		}
		return
	}
	cfg, _, err := c.buildTurnConfig(turn.message)
	if err != nil {
		c.rt.logger.Printf("conversation %s: compact unavailable (%T)", shortKey(c.key), err)
		if !turn.noAck {
			c.replyCommand(turn, "This conversation is temporarily unavailable.")
		}
		return
	}
	select {
	case c.rt.runs <- struct{}{}:
		defer func() { <-c.rt.runs }()
	default:
		if !turn.noAck {
			c.replyCommand(turn, "Busy; try again shortly.")
		}
		return
	}
	ctx, cancel := context.WithCancel(c.ctx)
	if c.rt.runTimeout > 0 {
		ctx, cancel = context.WithTimeout(c.ctx, c.rt.runTimeout)
	}
	c.mu.Lock()
	c.running = true
	c.runStop = cancel
	loop := c.loop
	c.mu.Unlock()
	err = loop.Compact(ctx, cfg, "")
	c.mu.Lock()
	c.running = false
	c.runStop = nil
	c.last = time.Now()
	c.mu.Unlock()
	cancel()
	c.persistActivity()
	reply := "Compacted."
	switch {
	case err == nil:
	case agent.IsCompactionNoWork(err):
		reply = "Nothing to compact."
	case errors.Is(err, context.Canceled):
		reply = "Compaction stopped."
	default:
		c.rt.logger.Printf("conversation %s: compaction failed (%T)", shortKey(c.key), err)
		reply = "Compaction failed."
	}
	if !turn.noAck {
		c.replyCommand(turn, reply)
	}
}
