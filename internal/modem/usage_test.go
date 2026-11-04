package modem

import (
	"testing"
	"time"
)

func TestPeriodStart(t *testing.T) {
	loc := time.UTC
	for _, c := range []struct {
		now  time.Time
		day  int
		want string
	}{
		{time.Date(2026, 11, 5, 10, 0, 0, 0, loc), 1, "2026-11-01"},
		{time.Date(2026, 11, 5, 10, 0, 0, 0, loc), 15, "2026-10-15"},
		{time.Date(2026, 11, 15, 0, 0, 0, 0, loc), 15, "2026-11-15"},
		{time.Date(2026, 1, 3, 0, 0, 0, 0, loc), 10, "2025-12-10"},
	} {
		if got := periodStart(c.now, c.day).Format("2006-01-02"); got != c.want {
			t.Errorf("periodStart(%v, %d) = %s, want %s", c.now, c.day, got, c.want)
		}
	}
}

func TestAddSample(t *testing.T) {
	var u Usage
	u.addSample(1000) // first reading: baseline only
	if u.Bytes != 0 {
		t.Fatalf("first sample counted %d", u.Bytes)
	}
	u.addSample(1500)
	u.addSample(2500)
	if u.Bytes != 1500 {
		t.Fatalf("bytes %d, want 1500", u.Bytes)
	}
	u.addSample(300) // counter restarted: 300 new bytes
	if u.Bytes != 1800 || u.LastCounter != 300 {
		t.Fatalf("after reset: %+v", u)
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{
		"10GB": 10e9, "10 gb": 10e9, "1.5G": 1.5e9, "500MB": 500e6, "2,5 GB": 2.5e9, "1T": 1e12, "123": 123,
	} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "-5GB", "0"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) accepted", bad)
		}
	}
	if FormatSize(1234567890) != "1.23 GB" || FormatSize(5e6) != "5 MB" || FormatSize(10e9) != "10 GB" || FormatSize(1.5e9) != "1.5 GB" {
		t.Errorf("FormatSize: %s %s", FormatSize(1234567890), FormatSize(5e6))
	}
}

func TestSampleUsageNewPeriodResets(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := saveUsageForTest(Usage{Bytes: 5e9, PeriodStart: time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local), Warned: true}); err != nil {
		t.Fatal(err)
	}
	u, err := SampleUsage(time.Date(2026, 11, 5, 12, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatal(err)
	}
	if u.Bytes != 0 || u.Warned || u.PeriodStart.Format("2006-01-02") != "2026-11-01" || u.NextReset.Format("2006-01-02") != "2026-12-01" {
		t.Fatalf("new period not started: %+v", u)
	}
}

func saveUsageForTest(u Usage) error {
	unlock, err := lockUsage()
	if err != nil {
		return err
	}
	defer unlock()
	return saveUsage(u)
}
