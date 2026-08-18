-- 0002_publish_crop.sql — per-camera publish crop (change request 2026-08-18).
-- Same per-component rules as scoring ROI; the compound x+w<=1 / y+h<=1
-- constraints cannot be added by ALTER in SQLite and are enforced at the
-- application layer (store write path + engine fallback to full frame).

ALTER TABLE cameras ADD COLUMN publish_crop_x REAL
CHECK (publish_crop_x IS NULL OR (publish_crop_x >= 0 AND publish_crop_x <= 1));
ALTER TABLE cameras ADD COLUMN publish_crop_y REAL
CHECK (publish_crop_y IS NULL OR (publish_crop_y >= 0 AND publish_crop_y <= 1));
ALTER TABLE cameras ADD COLUMN publish_crop_w REAL
CHECK (publish_crop_w IS NULL OR (publish_crop_w >= 0 AND publish_crop_w <= 1));
ALTER TABLE cameras ADD COLUMN publish_crop_h REAL
CHECK (publish_crop_h IS NULL OR (publish_crop_h >= 0 AND publish_crop_h <= 1));
