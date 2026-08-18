-- 0004_forecast_components.sql — record heuristic inputs/intermediates so
-- the openmeteo-h1 coefficients can be retuned against observed outcomes.

ALTER TABLE forecast_observations ADD COLUMN components_json TEXT;
