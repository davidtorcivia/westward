-- 0005_labels.sql — operator ground-truth labels for sunset/not-sunset.
-- Diagnostics are snapshotted at tag time so old labels stay meaningful
-- even after frames roll off retention.

CREATE TABLE frame_labels (
  id INTEGER PRIMARY KEY,
  camera_id TEXT NOT NULL REFERENCES cameras(id),
  kind TEXT NOT NULL CHECK(kind IN ('sunset','not_sunset')),
  tagged_utc INTEGER NOT NULL,
  local_date TEXT NOT NULL,
  score REAL, sunset_pixel_fraction REAL, median_l REAL, mean_chroma REAL,
  scoring_version TEXT, notes TEXT
);
CREATE INDEX frame_labels_camera ON frame_labels(camera_id, kind);
