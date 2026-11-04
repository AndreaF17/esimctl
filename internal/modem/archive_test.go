package modem

import (
	"testing"
	"time"
)

func TestArchiveKeepsMessagesAndReadState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := Message{Number: "+39123", Text: "ciao", Time: time.Date(2026, 11, 4, 22, 49, 0, 0, time.UTC), Incoming: true}
	Archive(m)
	Archive(m) // idempotent
	if got := loadArchive(); len(got) != 1 {
		t.Fatalf("archive has %d messages", len(got))
	}
	MarkRead(m)
	if !loadRead()[m.key()] {
		t.Fatal("read state lost")
	}
}
