-- 0001_init.sql — initial schema. Timestamps are integer Unix milliseconds, UTC.
-- Local dates are YYYY-MM-DD strings in the configured location.

CREATE TABLE cameras (
  id TEXT PRIMARY KEY,                    -- ULID
  name TEXT NOT NULL,
  type TEXT NOT NULL CHECK(type IN ('httpjpeg','nyctmc')),
  ref TEXT NOT NULL,                      -- URL (httpjpeg) or DOT id (nyctmc)
  enabled INTEGER NOT NULL DEFAULT 0,
  role TEXT NOT NULL DEFAULT 'trigger_only'
    CHECK(role IN ('publish_primary','publish_backup','trigger_only')),
  publish_priority INTEGER NOT NULL DEFAULT 0,
  -- Operator holds a signed DOT data-sharing agreement; see README deviation note.
  -- nyctmc rows default publish_eligible=1, set explicitly by admin code on insert.
  publish_eligible INTEGER NOT NULL DEFAULT 1 CHECK(publish_eligible IN (0,1)),
  attribution TEXT,
  roi_x REAL, roi_y REAL, roi_w REAL, roi_h REAL,
  threshold_abs REAL NOT NULL DEFAULT 12.0,
  trigger_json TEXT,                      -- per-camera overrides: {"ratio":..,"delta_abs":..,"rise_delta":..}
  credential_ref TEXT,
  headers_json TEXT,
  state TEXT NOT NULL DEFAULT 'ok' CHECK(state IN ('ok','stale')),
  stale_streak INTEGER NOT NULL DEFAULT 0,
  created_utc INTEGER NOT NULL,
  updated_utc INTEGER NOT NULL,
  CHECK(roi_x IS NULL OR (roi_x >= 0 AND roi_x <= 1)),
  CHECK(roi_y IS NULL OR (roi_y >= 0 AND roi_y <= 1)),
  CHECK(roi_w IS NULL OR (roi_w >= 0 AND roi_w <= 1)),
  CHECK(roi_h IS NULL OR (roi_h >= 0 AND roi_h <= 1)),
  CHECK(roi_x IS NULL OR roi_w IS NULL OR roi_x + roi_w <= 1.000001),
  CHECK(roi_y IS NULL OR roi_h IS NULL OR roi_y + roi_h <= 1.000001)
);

CREATE TABLE capture_runs (
  id TEXT PRIMARY KEY,
  mode TEXT NOT NULL CHECK(mode IN ('production','debug')),
  local_date TEXT NOT NULL,
  planned_start_utc INTEGER,
  planned_end_utc INTEGER,
  actual_start_utc INTEGER,
  actual_end_utc INTEGER,
  config_revision INTEGER,
  scoring_version TEXT,
  status TEXT NOT NULL
    CHECK(status IN ('running','finished','interrupted','deleted')),
  resumed_from TEXT
);
CREATE INDEX capture_runs_date ON capture_runs(local_date, mode);

CREATE TABLE frames (
  id INTEGER PRIMARY KEY,
  run_id TEXT NOT NULL REFERENCES capture_runs(id),
  camera_id TEXT NOT NULL REFERENCES cameras(id),
  local_date TEXT NOT NULL,
  fetched_utc INTEGER NOT NULL,
  width INTEGER, height INTEGER,
  sha256 TEXT NOT NULL,
  score REAL,
  sunset_pixel_fraction REAL,
  median_l REAL,
  mean_chroma REAL,
  scoring_version TEXT NOT NULL,
  valid TEXT NOT NULL DEFAULT 'ok'
    CHECK(valid IN ('ok','invalid_roi','decode_error','oversize','duplicate','low_disk','missing_file')),
  path TEXT NOT NULL UNIQUE
);
CREATE INDEX frames_best ON frames(local_date, camera_id, score DESC);
CREATE INDEX frames_run ON frames(run_id);
CREATE INDEX frames_sha ON frames(camera_id, sha256);

CREATE TABLE days (
  date TEXT PRIMARY KEY,
  status TEXT NOT NULL CHECK(status IN ('scheduled','capturing','complete','missed','failed')),
  reason TEXT,
  best_score REAL,
  best_camera_id TEXT REFERENCES cameras(id),
  best_taken_utc INTEGER,
  best_path TEXT,
  thumb480_path TEXT,
  thumb240_path TEXT,
  completed_utc INTEGER
);

CREATE TABLE forecast_observations (
  id INTEGER PRIMARY KEY,
  local_date TEXT NOT NULL,
  provider TEXT NOT NULL,
  fetched_utc INTEGER NOT NULL,
  event_utc INTEGER,
  lead_minutes INTEGER,
  quality REAL,
  detail TEXT,
  raw_json TEXT NOT NULL,
  algorithm_version TEXT NOT NULL,
  selected INTEGER NOT NULL DEFAULT 0 CHECK(selected IN (0,1))
);
CREATE INDEX forecast_obs_date ON forecast_observations(local_date, provider);

CREATE TABLE alert_events (
  id TEXT PRIMARY KEY,                    -- ULID
  event_key TEXT NOT NULL UNIQUE,         -- '{local_date}:go' | '{local_date}:headsup' | 'test:{ulid}' | 'system:{date}:{kind}'
  local_date TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('headsup','go','test','system')),
  title TEXT NOT NULL,
  body TEXT NOT NULL,
  image_path TEXT,
  metadata_json TEXT,
  created_utc INTEGER NOT NULL
);

CREATE TABLE alert_deliveries (
  event_id TEXT NOT NULL REFERENCES alert_events(id),
  notifier_id TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('pending','sending','sent','failed')),
  attempts INTEGER NOT NULL DEFAULT 0,
  last_error TEXT,
  sent_utc INTEGER,
  PRIMARY KEY (event_id, notifier_id)
);
CREATE INDEX alert_deliveries_pending ON alert_deliveries(state);

CREATE TABLE nyctmc_cameras (
  dot_id TEXT PRIMARY KEY,
  name TEXT,
  lat REAL, lon REAL,
  online INTEGER,
  refreshed_utc INTEGER NOT NULL
);

CREATE TABLE settings (
  key TEXT PRIMARY KEY,
  value_json TEXT NOT NULL,
  updated_utc INTEGER NOT NULL,
  revision INTEGER NOT NULL
);
