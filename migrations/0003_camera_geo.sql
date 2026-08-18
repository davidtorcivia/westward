-- 0003_camera_geo.sql — camera coordinates for the admin map view.
-- nyctmc rows are populated from the DOT list cache; httpjpeg rows are
-- optional operator-entered positions.

ALTER TABLE cameras ADD COLUMN lat REAL;
ALTER TABLE cameras ADD COLUMN lon REAL;
