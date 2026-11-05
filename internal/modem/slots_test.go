package modem

import (
	"strings"
	"testing"
)

func TestVisibleSlots(t *testing.T) {
	st := Status{State: "failed", FailedReason: "sim-missing", PrimarySlot: 1, Slots: []Slot{
		{Index: 1},
		{Index: 2, Present: true, Type: "esim"},
	}}
	if v := st.VisibleSlots(false); len(v) != 1 || v[0].Index != 2 {
		t.Errorf("hidden: got %+v, want only the eSIM", v)
	}
	if v := st.VisibleSlots(true); len(v) != 2 {
		t.Errorf("shown: got %d slots, want 2", len(v))
	}
	if p := st.Problem(true); !strings.HasPrefix(p, "no SIM card inserted; switch to the eSIM") || !strings.HasSuffix(p, "slot 2") {
		t.Errorf("Problem(hide) = %q", p)
	}
	if p := st.Problem(false); !strings.HasPrefix(p, "no SIM in slot 1") {
		t.Errorf("Problem(show) = %q", p)
	}

	// Nothing inserted anywhere: every slot shows.
	empty := Status{Slots: []Slot{{Index: 1}, {Index: 2}}}
	if v := empty.VisibleSlots(false); len(v) != 2 {
		t.Errorf("all empty: got %d slots, want 2", len(v))
	}
}
