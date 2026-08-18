package store

import (
	"path/filepath"
	"testing"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestMigrateIdempotent(t *testing.T) {
	_, path := open(t)
	// Opening again (same path) must not fail or duplicate migrations.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s2.Close()
}

func TestSettingsRoundTripAndRevision(t *testing.T) {
	s, _ := open(t)

	// Absent row -> defaults, revision 0.
	got, rev, err := s.GetSettings()
	if err != nil || rev != 0 {
		t.Fatalf("rev=%d err=%v", rev, err)
	}
	if got.QualityFloor != 45 || got.Capture.IntervalS != 45 {
		t.Fatalf("defaults wrong: %+v", got)
	}

	// Save bumps revision to 1, persists.
	got.QualityFloor = 50
	got.Lat = 41
	rev1, err := s.SaveSettings(got)
	if err != nil {
		t.Fatal(err)
	}
	if rev1 != 1 {
		t.Fatalf("first save revision = %d, want 1", rev1)
	}
	got2, rev, err := s.GetSettings()
	if err != nil || rev != 1 {
		t.Fatalf("rev=%d err=%v", rev, err)
	}
	if got2.QualityFloor != 50 || got2.Lat != 41 {
		t.Fatalf("round trip lost data: %+v", got2)
	}

	// Second save bumps to 2.
	if _, err := s.SaveSettings(got2); err != nil {
		t.Fatal(err)
	}
	if _, rev, _ := s.GetSettings(); rev != 2 {
		t.Fatalf("second save revision = %d, want 2", rev)
	}

	// Invalid settings rejected without touching revision.
	bad := got2
	bad.Capture.IntervalS = 5
	if _, err := s.SaveSettings(bad); err == nil {
		t.Fatal("interval 5s accepted")
	}
	if _, rev, _ := s.GetSettings(); rev != 2 {
		t.Fatalf("revision changed on rejected save: %d", rev)
	}
}

func TestRawSettings(t *testing.T) {
	s, _ := open(t)
	var out string
	if ok, _ := s.GetSettingRaw("install_date", &out); ok {
		t.Fatal("install_date present before set")
	}
	if err := s.SetSettingRaw("install_date", "2026-08-18"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.GetSettingRaw("install_date", &out); !ok || err != nil || out != "2026-08-18" {
		t.Fatalf("ok=%v err=%v out=%q", ok, err, out)
	}
}
