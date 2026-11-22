package main

import (
	"fmt"
	"strings"
)

// commandText recognizes the small native command set. Mention and whitespace
// normalization happens in the transport before this check.
func commandText(text string) (string, bool) {
	command := strings.ToLower(strings.TrimSpace(text))
	switch command {
	case "/new", "/reset", "/stop", "/status":
		return command, true
	default:
		return "", false
	}
}

// handleCommand reports whether the turn was a command. Only an explicit
// owner may reset or inspect a conversation.
func (c *conversation) handleCommand(turn sourceTurn, command string) bool {
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
		c.replyCommand(turn, fmt.Sprintf("Model: %s\nStatus: %s", c.rt.agent.Model, c.status()))
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
