package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadStaticDefaults(t *testing.T) {
	c, err := LoadStatic("")
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTP.Listen != ":8080" || c.DB.Path != "westward.db" || c.Data.Root != "./data" {
		t.Fatalf("bad defaults: %+v", c)
	}
}

func TestLoadStaticYAMLAndEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	os.WriteFile(path, []byte(`
http: { listen: ":9999" }
db: { path: /tmp/x.db }
location: { lat: 41.0, lon: -71.0, tz: America/Chicago }
notifiers:
  - { id: n1, type: ntfy, enabled: true, server: "https://ntfy.sh", topic: t }
`), 0o644)
	c, err := LoadStatic(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTP.Listen != ":9999" || c.Location.TZ != "America/Chicago" || len(c.Notifiers) != 1 {
		t.Fatalf("yaml not applied: %+v", c)
	}

	t.Setenv("WESTWARD_LISTEN", ":7777")
	c, err = LoadStatic(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTP.Listen != ":7777" {
		t.Fatalf("env override not applied: %q", c.HTTP.Listen)
	}
}

func TestLoadStaticRejectsDupes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	os.WriteFile(path, []byte(`
notifiers:
  - { id: n1, type: ntfy }
  - { id: n1, type: pushover }
`), 0o644)
	if _, err := LoadStatic(path); err == nil {
		t.Fatal("duplicate notifier ids accepted")
	}
}

func TestSettingsValidate(t *testing.T) {
	s := Defaults()
	if err := s.Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	cases := []func(*Settings){
		func(s *Settings) { s.Capture.IntervalS = 10 },
		func(s *Settings) { s.Lat = 200 },
		func(s *Settings) { s.TZ = "Not/AZone" },
		func(s *Settings) { s.QualityFloor = 101 },
		func(s *Settings) { s.Forecast.Provider = "accuweather" },
		func(s *Settings) { s.Trigger.Ratio = 0 },
	}
	for i, mutate := range cases {
		s := Defaults()
		mutate(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}

func TestSeed(t *testing.T) {
	s := Defaults()
	s.Seed(&LocationSeed{Lat: 41, Lon: -71, TZ: "America/Chicago"})
	if s.Lat != 41 || s.TZ != "America/Chicago" {
		t.Fatalf("seed not applied: %+v", s)
	}
	// nil seed is a no-op.
	s2 := Defaults()
	s2.Seed(nil)
	if s2.Lat != 40.6782 {
		t.Fatal("nil seed changed defaults")
	}
}
