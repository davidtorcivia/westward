// Package config defines startup-only configuration (YAML + env) and the
// runtime-editable settings persisted in the DB. Precedence: env secrets >
// DB runtime settings > YAML defaults.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Static is startup-only configuration: changing it requires a restart.
type Static struct {
	HTTP struct {
		Listen string `yaml:"listen"`
	} `yaml:"http"`
	DB struct {
		Path string `yaml:"path"`
	} `yaml:"db"`
	Data struct {
		Root string `yaml:"root"`
	} `yaml:"data"`
	Log struct {
		Level string `yaml:"level"`
	} `yaml:"log"`
	// Location seeds the runtime settings on first boot only (YAML defaults).
	Location *LocationSeed `yaml:"location,omitempty"`
	// Notifiers are static definitions; enablement is runtime-editable.
	Notifiers []NotifierDef `yaml:"notifiers"`
}

type LocationSeed struct {
	Lat float64 `yaml:"lat"`
	Lon float64 `yaml:"lon"`
	TZ  string  `yaml:"tz"`
}

// NotifierDef is one delivery channel. Secrets are env var NAMES, never values.
type NotifierDef struct {
	ID       string `yaml:"id"`
	Type     string `yaml:"type"` // ntfy | pushover | webhook
	Enabled  bool   `yaml:"enabled"`
	Server   string `yaml:"server"`    // ntfy
	Topic    string `yaml:"topic"`     // ntfy
	TokenEnv string `yaml:"token_env"` // ntfy (optional), pushover token, sunsethue
	UserEnv  string `yaml:"user_env"`  // pushover user key
	URL      string `yaml:"url"`       // webhook
	HMACEnv  string `yaml:"hmac_env"`  // webhook (optional; WESTWARD_WEBHOOK_HMAC_KEY)
}

// Settings is the runtime-editable configuration, stored as one JSON row in
// the settings table. Durations are integer seconds for human-editable JSON.
type Settings struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
	TZ  string  `json:"tz"`

	Capture struct {
		IntervalS int `json:"interval_s"`
		BeforeS   int `json:"before_s"`
		AfterS    int `json:"after_s"`
	} `json:"capture"`

	RetentionFramesDays int `json:"retention_frames_days"`
	DiskMinFreeMB       int `json:"disk_min_free_mb"`

	QualityFloor float64 `json:"quality_floor"`
	LateGraceS   int     `json:"late_grace_s"`

	Archive struct {
		CutoffAfterDuskS int     `json:"cutoff_after_dusk_s"`
		DarknessFloor    float64 `json:"darkness_floor"`
	} `json:"archive"`

	Trigger struct {
		ThresholdAbs float64 `json:"threshold_abs"`
		Ratio        float64 `json:"ratio"`
		DeltaAbs     float64 `json:"delta_abs"`
		RiseDelta    float64 `json:"rise_delta"`
	} `json:"trigger"`

	Forecast struct {
		Provider          string `json:"provider"` // openmeteo | sunsethue
		ComparisonEnabled bool   `json:"comparison_enabled"`
		OpenMeteo         struct {
			Tuning struct {
				Peak       float64 `json:"peak"`
				Width      float64 `json:"width"`
				LowPenalty float64 `json:"low_penalty"`
				HumPenalty float64 `json:"hum_penalty"`
				Overlap    float64 `json:"overlap"`
			} `json:"tuning"`
		} `json:"openmeteo"`
	} `json:"forecast"`

	NotifierEnabled map[string]bool `json:"notifier_enabled"`
}

// Defaults returns the shipped settings for Brooklyn, NY.
func Defaults() Settings {
	s := Settings{
		Lat:                 40.6782,
		Lon:                 -73.9442,
		TZ:                  "America/New_York",
		RetentionFramesDays: 5,
		DiskMinFreeMB:       2048,
		QualityFloor:        45,
		LateGraceS:          600,
	}
	s.Capture.IntervalS = 45
	s.Capture.BeforeS = 1800
	s.Capture.AfterS = 1200
	s.Archive.CutoffAfterDuskS = 300
	s.Archive.DarknessFloor = 10
	s.Trigger.ThresholdAbs = 12.0
	s.Trigger.Ratio = 1.6
	s.Trigger.DeltaAbs = 4.0
	s.Trigger.RiseDelta = 1.5
	s.Forecast.Provider = "openmeteo"
	s.Forecast.ComparisonEnabled = true
	s.Forecast.OpenMeteo.Tuning.Peak = 0.45
	s.Forecast.OpenMeteo.Tuning.Width = 0.25
	s.Forecast.OpenMeteo.Tuning.LowPenalty = 55
	s.Forecast.OpenMeteo.Tuning.HumPenalty = 0.4
	s.Forecast.OpenMeteo.Tuning.Overlap = 0.5
	return s
}

// Validate rejects settings that would break scheduling or scoring.
func (s *Settings) Validate() error {
	if s.Lat < -90 || s.Lat > 90 || s.Lon < -180 || s.Lon > 180 {
		return fmt.Errorf("lat/lon out of range")
	}
	if _, err := time.LoadLocation(s.TZ); err != nil {
		return fmt.Errorf("bad tz %q: %w", s.TZ, err)
	}
	if s.Capture.IntervalS < 15 {
		return fmt.Errorf("capture interval below 15s minimum")
	}
	if s.Capture.BeforeS <= 0 || s.Capture.AfterS <= 0 {
		return fmt.Errorf("capture window must be positive")
	}
	if s.RetentionFramesDays < 1 || s.DiskMinFreeMB < 0 {
		return fmt.Errorf("bad retention/disk settings")
	}
	if s.QualityFloor < 0 || s.QualityFloor > 100 {
		return fmt.Errorf("quality floor must be 0..100")
	}
	if s.Trigger.Ratio <= 0 || s.Trigger.ThresholdAbs < 0 {
		return fmt.Errorf("bad trigger settings")
	}
	if s.Forecast.Provider != "openmeteo" && s.Forecast.Provider != "sunsethue" {
		return fmt.Errorf("unknown forecast provider %q", s.Forecast.Provider)
	}
	return nil
}

// LoadStatic reads the YAML file (if path non-empty) then applies env
// overrides. Enriches nothing else; caller validates admin secrets.
func LoadStatic(path string) (Static, error) {
	var c Static
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return c, err
		}
		if err := yaml.Unmarshal(b, &c); err != nil {
			return c, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	if v := os.Getenv("WESTWARD_LISTEN"); v != "" {
		c.HTTP.Listen = v
	}
	if v := os.Getenv("WESTWARD_DB_PATH"); v != "" {
		c.DB.Path = v
	}
	if v := os.Getenv("WESTWARD_DATA_ROOT"); v != "" {
		c.Data.Root = v
	}
	if c.HTTP.Listen == "" {
		c.HTTP.Listen = ":8080"
	}
	if c.DB.Path == "" {
		c.DB.Path = "westward.db"
	}
	if c.Data.Root == "" {
		c.Data.Root = "./data"
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	seen := map[string]bool{}
	for _, n := range c.Notifiers {
		if n.ID == "" || seen[n.ID] {
			return c, fmt.Errorf("notifier ids must be unique and non-empty")
		}
		seen[n.ID] = true
		switch n.Type {
		case "ntfy", "pushover", "webhook":
		default:
			return c, fmt.Errorf("notifier %q: unknown type %q", n.ID, n.Type)
		}
	}
	return c, nil
}

// Seed applies YAML location defaults to first-boot settings.
func (s *Settings) Seed(loc *LocationSeed) {
	if loc == nil {
		return
	}
	if loc.Lat != 0 {
		s.Lat = loc.Lat
	}
	if loc.Lon != 0 {
		s.Lon = loc.Lon
	}
	if loc.TZ != "" {
		s.TZ = loc.TZ
	}
}
