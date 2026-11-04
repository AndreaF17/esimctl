package main

import (
	"context"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"

	"github.com/AndreaF17/esimctl/internal/euicc"
)

func newTestModel() model {
	return model{ctx: context.Background(), st: newStyles(), input: textinput.New(), pending: map[string]string{}}
}

// enter simulates typing v into the current input and pressing enter.
func enter(t *testing.T, m model, p inputPurpose, v string) model {
	t.Helper()
	m.purpose, m.mode = p, modeInput
	out, _ := m.submit(v)
	return out.(model)
}

func TestPINValidation(t *testing.T) {
	m := enter(t, newTestModel(), inputPIN, "12a4")
	if m.busy || m.statusOK || m.status == "" {
		t.Fatalf("invalid PIN accepted: busy=%v status=%q", m.busy, m.status)
	}
	m = enter(t, newTestModel(), inputPIN, "1234")
	if !m.busy || m.stage != "unlocking the SIM" {
		t.Fatalf("valid PIN not submitted: busy=%v stage=%q", m.busy, m.stage)
	}
}

func TestChangePINFlow(t *testing.T) {
	m := enter(t, newTestModel(), inputOldPIN, "1234")
	if m.mode != modeInput || m.purpose != inputNewPIN {
		t.Fatalf("expected new PIN prompt, got purpose %v mode %v", m.purpose, m.mode)
	}
	m = enter(t, m, inputNewPIN, "5678")
	if m.purpose != inputNewPINAgain {
		t.Fatalf("expected confirmation prompt, got %v", m.purpose)
	}
	mismatch := enter(t, m, inputNewPINAgain, "5679")
	if mismatch.busy || mismatch.status != "the two new PINs don't match" || len(mismatch.pending) != 0 {
		t.Fatalf("mismatch: busy=%v status=%q pending=%v", mismatch.busy, mismatch.status, mismatch.pending)
	}
	m.pending = map[string]string{"old": "1234", "new": "5678"}
	ok := enter(t, m, inputNewPINAgain, "5678")
	if !ok.busy || ok.stage != "changing the PIN" || len(ok.pending) != 0 {
		t.Fatalf("change not submitted: busy=%v stage=%q pending=%v", ok.busy, ok.stage, ok.pending)
	}
}

func TestPUKFlow(t *testing.T) {
	if m := enter(t, newTestModel(), inputPUK, "1234"); m.status != "the PUK must be 8 digits" {
		t.Fatalf("short PUK: %q", m.status)
	}
	m := enter(t, newTestModel(), inputPUK, "12345678")
	if m.purpose != inputPUKNewPIN || m.pending["puk"] != "12345678" {
		t.Fatalf("expected new PIN prompt: %v %v", m.purpose, m.pending)
	}
	m = enter(t, m, inputPUKNewPIN, "0000")
	if !m.busy || m.stage != "unblocking the SIM" || len(m.pending) != 0 {
		t.Fatalf("PUK not submitted: busy=%v stage=%q", m.busy, m.stage)
	}
}

func TestDeleteNeedsTypedConfirmation(t *testing.T) {
	m := newTestModel()
	m.profiles = []euicc.Profile{{ICCID: "89001010000000000026", Name: "Test"}}
	for _, v := range []string{"", "y", "yes", "del"} {
		got := enter(t, m, inputDelete, v)
		if got.busy || got.status != "not deleted" {
			t.Fatalf("%q deleted the profile", v)
		}
	}
	if got := enter(t, m, inputDelete, "DELETE"); !got.busy || got.stage != "deleting Test" {
		t.Fatalf("typed confirmation ignored: busy=%v stage=%q", got.busy, got.stage)
	}
}
