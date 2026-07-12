package wire

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// OutMsg / EncodeOut / DecodeOut
// ---------------------------------------------------------------------------

func TestEncodeDecodeOutRoundTrip(t *testing.T) {
	cases := []OutMsg{
		{Type: "status", Status: "running", Detail: "warming up"},
		{Type: "message", To: "lead", Text: "hello there"},
		{Type: "task", TaskID: "task_1", Status: "completed", Text: "done"},
		{Type: "subagent", Name: "helper", Role: "tester", Status: "running"},
		{Type: "approval", Action: "run_command", Reason: "need to rm files", Risk: "high"},
		{Type: "log", Text: "a notable log line"},
	}

	for _, m := range cases {
		t.Run(m.Type, func(t *testing.T) {
			line := EncodeOut(m)
			got, ok := DecodeOut(line)
			if !ok {
				t.Fatalf("DecodeOut(%q) ok = false, want true", line)
			}
			if got != m {
				t.Errorf("round trip = %+v, want %+v", got, m)
			}
		})
	}
}

func TestDecodeOutRejectsPlainText(t *testing.T) {
	_, ok := DecodeOut("just some ordinary terminal output")
	if ok {
		t.Error("DecodeOut(plain text) ok = true, want false")
	}
}

func TestDecodeOutRejectsBadJSON(t *testing.T) {
	_, ok := DecodeOut(Marker + "{not valid json")
	if ok {
		t.Error("DecodeOut(marker+bad json) ok = true, want false")
	}
}

func TestDecodeOutRejectsEmptyType(t *testing.T) {
	_, ok := DecodeOut(Marker + `{"status":"running"}`)
	if ok {
		t.Error("DecodeOut(marker with empty type) ok = true, want false")
	}
}

func TestDecodeOutAcceptsWhitespaceAndCRLF(t *testing.T) {
	line := "   " + Marker + `{"type":"status","status":"running"}` + "\r\n"
	got, ok := DecodeOut(line)
	if !ok {
		t.Fatalf("DecodeOut(padded line) ok = false, want true")
	}
	if got.Type != "status" || got.Status != "running" {
		t.Errorf("got = %+v, want type=status status=running", got)
	}
}

func TestDecodeOutRejectsEmptyLine(t *testing.T) {
	_, ok := DecodeOut("")
	if ok {
		t.Error("DecodeOut(\"\") ok = true, want false")
	}
}

// ---------------------------------------------------------------------------
// Envelope / EncodeEnvelope / DecodeEnvelope
// ---------------------------------------------------------------------------

func TestEncodeDecodeEnvelopeRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	env := Envelope{
		From:      "lead",
		Text:      "please implement the widget",
		Summary:   "please implement the widget",
		Timestamp: now,
		MsgID:     "msg_1",
		Type:      "message",
		TaskID:    "task_1",
		ReplyTo:   "msg_0",
	}

	line := EncodeEnvelope(env)
	got, ok := DecodeEnvelope(line)
	if !ok {
		t.Fatalf("DecodeEnvelope(%q) ok = false, want true", line)
	}
	if got.From != env.From || got.Text != env.Text || got.Summary != env.Summary ||
		got.MsgID != env.MsgID || got.Type != env.Type || got.TaskID != env.TaskID ||
		got.ReplyTo != env.ReplyTo {
		t.Errorf("round trip = %+v, want %+v", got, env)
	}
	if !got.Timestamp.Equal(env.Timestamp) {
		t.Errorf("Timestamp = %v, want %v", got.Timestamp, env.Timestamp)
	}
}

func TestDecodeEnvelopeRejectsMissingMsgID(t *testing.T) {
	line := `{"from":"lead","text":"hi","type":"message"}`
	_, ok := DecodeEnvelope(line)
	if ok {
		t.Error("DecodeEnvelope(missing msg_id) ok = true, want false")
	}
}

func TestDecodeEnvelopeRejectsBadJSON(t *testing.T) {
	_, ok := DecodeEnvelope("{not valid json")
	if ok {
		t.Error("DecodeEnvelope(bad json) ok = true, want false")
	}
}

func TestDecodeEnvelopeTrimsWhitespace(t *testing.T) {
	line := "   " + `{"from":"lead","text":"hi","msg_id":"m1","type":"message"}` + "  \n"
	got, ok := DecodeEnvelope(line)
	if !ok {
		t.Fatalf("DecodeEnvelope(padded) ok = false, want true")
	}
	if got.MsgID != "m1" {
		t.Errorf("MsgID = %q, want %q", got.MsgID, "m1")
	}
}
