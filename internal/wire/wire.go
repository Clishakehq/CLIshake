// Package wire defines clishake's line protocols shared between in-pane
// agent processes and the orchestrator:
//
//  1. Structured output lines: an agent process (e.g. the mock agent, or a
//     wrapper around a real harness) can emit lines of the form
//
//     ##clishake:{"type":"status","status":"running",...}
//
//     on stdout. Pane output is piped to a log file; the adapter's
//     ParseOutput recognizes these lines and turns them into structured
//     events. Everything else on stdout is ordinary terminal output.
//
//  2. Inbox envelopes: agents with the structured_input capability receive
//     messages as JSONL envelopes appended to their inbox file at
//     .clishake/agents/<id>/inbox.jsonl. This mirrors the mailbox files
//     used by Claude Code agent teams (append by sender, drain by reader).
package wire

import (
	"encoding/json"
	"strings"
	"time"
)

// Marker prefixes every structured output line.
const Marker = "##clishake:"

// OutMsg is the payload of one structured output line.
//
// Types and their fields:
//
//	status:    Status (required), Detail
//	message:   To (required), Text (required)  — agent-to-agent / to lead
//	task:      TaskID, Status ("in_progress"|"completed"|...), Text
//	subagent:  Name (required), Role, Status   — sub-agent lifecycle report
//	approval:  Action (required), Reason, Risk
//	log:       Text
type OutMsg struct {
	Type   string `json:"type"`
	Status string `json:"status,omitempty"`
	Detail string `json:"detail,omitempty"`
	To     string `json:"to,omitempty"`
	Text   string `json:"text,omitempty"`
	TaskID string `json:"task_id,omitempty"`
	Name   string `json:"name,omitempty"`
	Role   string `json:"role,omitempty"`
	Action string `json:"action,omitempty"`
	Reason string `json:"reason,omitempty"`
	Risk   string `json:"risk,omitempty"`
}

// EncodeOut renders m as one structured output line (without newline).
func EncodeOut(m OutMsg) string {
	b, _ := json.Marshal(m)
	return Marker + string(b)
}

// DecodeOut parses a single line. ok is false when the line is not a
// well-formed structured output line (ordinary output, or corrupt JSON —
// callers must treat those as plain text, never guess).
func DecodeOut(line string) (OutMsg, bool) {
	line = strings.TrimRight(line, "\r\n")
	rest, found := strings.CutPrefix(strings.TrimSpace(line), Marker)
	if !found {
		return OutMsg{}, false
	}
	var m OutMsg
	if err := json.Unmarshal([]byte(rest), &m); err != nil || m.Type == "" {
		return OutMsg{}, false
	}
	return m, true
}

// Envelope is one inbox JSONL entry, closely modeled on the Claude teams
// mailbox schema (from, text, timestamp, msg_id, type).
type Envelope struct {
	From      string    `json:"from"`
	Text      string    `json:"text"`
	Summary   string    `json:"summary,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	MsgID     string    `json:"msg_id"`
	Type      string    `json:"type"` // "message" for chat and control alike
	TaskID    string    `json:"task_id,omitempty"`
	ReplyTo   string    `json:"reply_to,omitempty"`
}

// EncodeEnvelope renders e as one JSONL line (without newline).
func EncodeEnvelope(e Envelope) string {
	b, _ := json.Marshal(e)
	return string(b)
}

// DecodeEnvelope parses one inbox line.
func DecodeEnvelope(line string) (Envelope, bool) {
	var e Envelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &e); err != nil || e.MsgID == "" {
		return Envelope{}, false
	}
	return e, true
}
