package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/davidtorcivia/westward/internal/ulid"
)

// Camera mirrors the cameras table.
type Camera struct {
	ID                         string
	Name                       string
	Type                       string // httpjpeg | nyctmc
	Ref                        string // URL or DOT id
	Enabled                    bool
	Role                       string // publish_primary | publish_backup | trigger_only
	PublishPriority            int
	PublishEligible            bool
	Attribution                string
	ROIX, ROIY, ROIW, ROIH     *float64
	CropX, CropY, CropW, CropH *float64
	Lat, Lon                   *float64
	ThresholdAbs               float64
	TriggerJSON                string // overrides {"ratio","delta_abs","rise_delta"}
	CredentialRef              string
	HeadersJSON                string
	State                      string // ok | stale
	StaleStreak                int
	CreatedUTC                 int64
	UpdatedUTC                 int64
}

const cameraCols = `id,name,type,ref,enabled,role,publish_priority,publish_eligible,attribution,
roi_x,roi_y,roi_w,roi_h,publish_crop_x,publish_crop_y,publish_crop_w,publish_crop_h,
threshold_abs,trigger_json,credential_ref,headers_json,state,stale_streak,created_utc,updated_utc,
lat,lon`

func scanCamera(row interface{ Scan(...any) error }) (Camera, error) {
	var c Camera
	var roiX, roiY, roiW, roiH sql.NullFloat64
	var cx, cy, cw, ch sql.NullFloat64
	var la, lo sql.NullFloat64
	var attr, trig, cred, hdr sql.NullString
	err := row.Scan(&c.ID, &c.Name, &c.Type, &c.Ref, &c.Enabled, &c.Role, &c.PublishPriority,
		&c.PublishEligible, &attr, &roiX, &roiY, &roiW, &roiH, &cx, &cy, &cw, &ch,
		&c.ThresholdAbs, &trig, &cred, &hdr,
		&c.State, &c.StaleStreak, &c.CreatedUTC, &c.UpdatedUTC, &la, &lo)
	if err != nil {
		return c, err
	}
	f := func(v sql.NullFloat64) *float64 {
		if v.Valid {
			x := v.Float64
			return &x
		}
		return nil
	}
	c.ROIX, c.ROIY, c.ROIW, c.ROIH = f(roiX), f(roiY), f(roiW), f(roiH)
	c.CropX, c.CropY, c.CropW, c.CropH = f(cx), f(cy), f(cw), f(ch)
	c.Lat, c.Lon = f(la), f(lo)
	c.Attribution, c.TriggerJSON, c.CredentialRef, c.HeadersJSON = attr.String, trig.String, cred.String, hdr.String
	return c, nil
}

func (s *Store) ListCameras() ([]Camera, error) {
	rows, err := s.db.Query(`SELECT ` + cameraCols + ` FROM cameras ORDER BY created_utc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Camera
	for rows.Next() {
		c, err := scanCamera(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetCamera(id string) (Camera, error) {
	c, err := scanCamera(s.db.QueryRow(`SELECT `+cameraCols+` FROM cameras WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

var ErrNotFound = errors.New("not found")

func (s *Store) InsertCamera(c *Camera) error {
	if c.ID == "" {
		c.ID = ulid.New(time.Now())
	}
	now := time.Now().UnixMilli()
	c.CreatedUTC, c.UpdatedUTC = now, now
	_, err := s.db.Exec(`INSERT INTO cameras(`+cameraCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Name, c.Type, c.Ref, c.Enabled, c.Role, c.PublishPriority, c.PublishEligible,
		nullStr(c.Attribution), nullF(c.ROIX), nullF(c.ROIY), nullF(c.ROIW), nullF(c.ROIH),
		nullF(c.CropX), nullF(c.CropY), nullF(c.CropW), nullF(c.CropH),
		c.ThresholdAbs, nullStr(c.TriggerJSON), nullStr(c.CredentialRef), nullStr(c.HeadersJSON),
		c.State, c.StaleStreak, c.CreatedUTC, c.UpdatedUTC, nullF(c.Lat), nullF(c.Lon))
	return err
}

func (s *Store) UpdateCamera(c *Camera) error {
	c.UpdatedUTC = time.Now().UnixMilli()
	_, err := s.db.Exec(`UPDATE cameras SET name=?,type=?,ref=?,enabled=?,role=?,publish_priority=?,
	publish_eligible=?,attribution=?,roi_x=?,roi_y=?,roi_w=?,roi_h=?,
	publish_crop_x=?,publish_crop_y=?,publish_crop_w=?,publish_crop_h=?,
	threshold_abs=?,trigger_json=?,
	credential_ref=?,headers_json=?,state=?,stale_streak=?,updated_utc=?,lat=?,lon=? WHERE id=?`,
		c.Name, c.Type, c.Ref, c.Enabled, c.Role, c.PublishPriority, c.PublishEligible,
		nullStr(c.Attribution), nullF(c.ROIX), nullF(c.ROIY), nullF(c.ROIW), nullF(c.ROIH),
		nullF(c.CropX), nullF(c.CropY), nullF(c.CropW), nullF(c.CropH),
		c.ThresholdAbs, nullStr(c.TriggerJSON), nullStr(c.CredentialRef), nullStr(c.HeadersJSON),
		c.State, c.StaleStreak, c.UpdatedUTC, nullF(c.Lat), nullF(c.Lon), c.ID)
	return err
}

func (s *Store) DeleteCamera(id string) error {
	_, err := s.db.Exec(`DELETE FROM cameras WHERE id=?`, id)
	return err
}

func (s *Store) BumpStaleStreak(cameraID string, delta int) error {
	_, err := s.db.Exec(`UPDATE cameras SET stale_streak=stale_streak+?, updated_utc=? WHERE id=?`,
		delta, time.Now().UnixMilli(), cameraID)
	return err
}

// ValidateCrop enforces the crop rules ALTER TABLE cannot express:
// all-or-none components and x+w<=1, y+h<=1. NaN/range checks live in the
// per-column CHECKs; pixel-size check runs at use time (score.PixelRect).
func ValidateCrop(c *Camera) error {
	set := c.CropX != nil || c.CropY != nil || c.CropW != nil || c.CropH != nil
	if !set {
		return nil
	}
	if c.CropX == nil || c.CropY == nil || c.CropW == nil || c.CropH == nil {
		return errors.New("crop: all four components required")
	}
	if *c.CropX+*c.CropW > 1.000001 || *c.CropY+*c.CropH > 1.000001 {
		return errors.New("crop: x+w or y+h exceeds 1")
	}
	return nil
}

func nullF(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullStr(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// Run mirrors capture_runs.
type Run struct {
	ID              string
	Mode            string // production | debug
	LocalDate       string
	PlannedStartUTC int64
	PlannedEndUTC   int64
	ActualStartUTC  int64
	ActualEndUTC    int64
	ConfigRevision  int64
	ScoringVersion  string
	Status          string // running | finished | interrupted | deleted
	ResumedFrom     string
}

func (s *Store) InsertRun(r *Run) error {
	if r.ID == "" {
		r.ID = ulid.New(time.Now())
	}
	_, err := s.db.Exec(`INSERT INTO capture_runs(id,mode,local_date,planned_start_utc,planned_end_utc,
	actual_start_utc,actual_end_utc,config_revision,scoring_version,status,resumed_from)
	VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Mode, r.LocalDate, nn(r.PlannedStartUTC), nn(r.PlannedEndUTC), nn(r.ActualStartUTC),
		nn(r.ActualEndUTC), nn(r.ConfigRevision), nullStr(r.ScoringVersion), r.Status, nullStr(r.ResumedFrom))
	return err
}

func nn(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func (s *Store) SetRunStatus(id, status string, actualEnd int64) error {
	_, err := s.db.Exec(`UPDATE capture_runs SET status=?, actual_end_utc=? WHERE id=?`,
		status, nn(actualEnd), id)
	return err
}

// nowMS is passed in so fake-clock tests match engine time.
func (s *Store) MarkInterruptedRuns(nowMS int64) (int, error) {
	res, err := s.db.Exec(`UPDATE capture_runs SET status='interrupted'
	WHERE mode='production' AND status='running' AND planned_end_utc < ?`, nowMS)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s *Store) LatestFinishedRun(localDate string) (Run, bool, error) {
	var r Run
	var ps, pe, as, ae, cr sql.NullInt64
	var sv, rf sql.NullString
	err := s.db.QueryRow(`SELECT id,mode,local_date,planned_start_utc,planned_end_utc,actual_start_utc,
	actual_end_utc,config_revision,scoring_version,status,resumed_from
	FROM capture_runs WHERE local_date=? AND mode='production' ORDER BY actual_start_utc DESC LIMIT 1`,
		localDate).Scan(&r.ID, &r.Mode, &r.LocalDate, &ps, &pe, &as, &ae, &cr, &sv, &r.Status, &rf)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	r.PlannedStartUTC, r.PlannedEndUTC, r.ActualStartUTC, r.ActualEndUTC, r.ConfigRevision =
		ps.Int64, pe.Int64, as.Int64, ae.Int64, cr.Int64
	r.ScoringVersion, r.ResumedFrom = sv.String, rf.String
	return r, true, nil
}

// FrameRow mirrors frames.
type FrameRow struct {
	ID                  int64
	RunID               string
	CameraID            string
	LocalDate           string
	FetchedUTC          int64
	Width, Height       int
	SHA256              string
	Score               *float64
	SunsetPixelFraction *float64
	MedianL             *float64
	MeanChroma          *float64
	ScoringVersion      string
	Valid               string
	Path                string
}

func (s *Store) InsertFrame(f *FrameRow) error {
	_, err := s.db.Exec(`INSERT INTO frames(run_id,camera_id,local_date,fetched_utc,width,height,sha256,
	score,sunset_pixel_fraction,median_l,mean_chroma,scoring_version,valid,path)
	VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.RunID, f.CameraID, f.LocalDate, f.FetchedUTC, f.Width, f.Height, f.SHA256,
		f.Score, f.SunsetPixelFraction, f.MedianL, f.MeanChroma,
		f.ScoringVersion, f.Valid, f.Path)
	return err
}

// PreviousFrameSHA returns the camera's most recent stored frame hash.
func (s *Store) PreviousFrameSHA(cameraID string) (string, bool, error) {
	var sha string
	err := s.db.QueryRow(`SELECT sha256 FROM frames WHERE camera_id=? AND valid IN ('ok')
	ORDER BY fetched_utc DESC LIMIT 1`, cameraID).Scan(&sha)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return sha, err == nil, err
}

// CameraDayFrames returns valid frames for one camera+date ordered by fetch time.
func (s *Store) CameraDayFrames(cameraID, localDate string) ([]FrameRow, error) {
	return s.frameRows(`SELECT id,run_id,camera_id,local_date,fetched_utc,width,height,sha256,
	score,sunset_pixel_fraction,median_l,mean_chroma,scoring_version,valid,path
	FROM frames WHERE camera_id=? AND local_date=? AND valid='ok' ORDER BY fetched_utc`, cameraID, localDate)
}

func (s *Store) frameRows(q string, args ...any) ([]FrameRow, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FrameRow
	for rows.Next() {
		var f FrameRow
		if err := rows.Scan(&f.ID, &f.RunID, &f.CameraID, &f.LocalDate, &f.FetchedUTC, &f.Width,
			&f.Height, &f.SHA256, &f.Score, &f.SunsetPixelFraction, &f.MedianL, &f.MeanChroma,
			&f.ScoringVersion, &f.Valid, &f.Path); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Day mirrors the days table.
type Day struct {
	Date         string
	Status       string
	Reason       string
	BestScore    *float64
	BestCameraID string
	BestTakenUTC int64
	BestPath     string
	Thumb480Path string
	Thumb240Path string
	CompletedUTC int64
}

func (s *Store) UpsertDayStart(date string) error {
	_, err := s.db.Exec(`INSERT INTO days(date,status) VALUES(?,'capturing')
	ON CONFLICT(date) DO UPDATE SET status='capturing' WHERE days.status IN ('scheduled','capturing')`, date)
	return err
}

func (s *Store) GetDay(date string) (Day, bool, error) {
	var d Day
	var reason sql.NullString
	var bs sql.NullFloat64
	var bc sql.NullString
	var bt, ct sql.NullInt64
	var t480, t240, bp sql.NullString
	err := s.db.QueryRow(`SELECT date,status,reason,best_score,best_camera_id,best_taken_utc,best_path,
	thumb480_path,thumb240_path,completed_utc FROM days WHERE date=?`, date).
		Scan(&d.Date, &d.Status, &reason, &bs, &bc, &bt, &bp, &t480, &t240, &ct)
	if errors.Is(err, sql.ErrNoRows) {
		return d, false, nil
	}
	if err != nil {
		return d, false, err
	}
	d.Reason = reason.String
	if bs.Valid {
		v := bs.Float64
		d.BestScore = &v
	}
	d.BestCameraID, d.BestTakenUTC = bc.String, bt.Int64
	d.BestPath, d.Thumb480Path, d.Thumb240Path, d.CompletedUTC = bp.String, t480.String, t240.String, ct.Int64
	return d, true, nil
}

// CompleteDay records finalization. status: complete | failed.
func (s *Store) CompleteDay(date, status, reason string, bestScore *float64, bestCamID string,
	bestTaken int64, bestPath, t480, t240 string) error {
	_, err := s.db.Exec(`UPDATE days SET status=?, reason=?, best_score=?, best_camera_id=?,
	best_taken_utc=?, best_path=?, thumb480_path=?, thumb240_path=?, completed_utc=?
	WHERE date=?`, status, nullStr(reason), bestScore, nullStr(bestCamID), nn(bestTaken),
		nullStr(bestPath), nullStr(t480), nullStr(t240), time.Now().UnixMilli(), date)
	return err
}

// BackfillMissedDays inserts status=missed rows for dates from installDate
// (inclusive) to before today that lack one. Returns count inserted.
func (s *Store) BackfillMissedDays(installDate, today string) (int, error) {
	if installDate >= today {
		return 0, nil
	}
	rows, err := s.db.Query(`SELECT date FROM days WHERE date >= ? AND date < ?`, installDate, today)
	if err != nil {
		return 0, err
	}
	have := map[string]bool{}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			rows.Close()
			return 0, err
		}
		have[d] = true
	}
	rows.Close()

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	n := 0
	for d := installDate; d < today; d = nextDate(d) {
		if have[d] {
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO days(date,status,reason) VALUES(?,'missed','no capture')`, d); err != nil {
			tx.Rollback()
			return 0, err
		}
		n++
	}
	return n, tx.Commit()
}

func nextDate(date string) string {
	t, _ := time.Parse("2006-01-02", date)
	return t.AddDate(0, 0, 1).Format("2006-01-02")
}

// AlertEvent mirrors alert_events.
type AlertEvent struct {
	ID           string
	EventKey     string
	LocalDate    string
	Kind         string
	Title        string
	Body         string
	ImagePath    string
	MetadataJSON string
	CreatedUTC   int64
}

// TryInsertEvent atomically claims event_key. Returns false when the key
// already exists (another camera won the daily latch).
func (s *Store) TryInsertEvent(e *AlertEvent, notifierIDs []string) (bool, error) {
	if e.ID == "" {
		e.ID = ulid.New(time.Now())
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	res, err := tx.Exec(`INSERT INTO alert_events(id,event_key,local_date,kind,title,body,image_path,metadata_json,created_utc)
	VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(event_key) DO NOTHING`,
		e.ID, e.EventKey, e.LocalDate, e.Kind, e.Title, e.Body, nullStr(e.ImagePath),
		nullStr(e.MetadataJSON), time.Now().UnixMilli())
	if err != nil {
		tx.Rollback()
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		tx.Rollback()
		return false, nil
	}
	for _, nid := range notifierIDs {
		if _, err := tx.Exec(`INSERT INTO alert_deliveries(event_id,notifier_id,state) VALUES(?,?,'pending')`,
			e.ID, nid); err != nil {
			tx.Rollback()
			return false, err
		}
	}
	return true, tx.Commit()
}

func (s *Store) GetEventByKey(eventKey string) (AlertEvent, bool, error) {
	var e AlertEvent
	var ip, mj sql.NullString
	err := s.db.QueryRow(`SELECT id,event_key,local_date,kind,title,body,image_path,metadata_json,created_utc
	FROM alert_events WHERE event_key=?`, eventKey).
		Scan(&e.ID, &e.EventKey, &e.LocalDate, &e.Kind, &e.Title, &e.Body, &ip, &mj, &e.CreatedUTC)
	if errors.Is(err, sql.ErrNoRows) {
		return e, false, nil
	}
	if err != nil {
		return e, false, err
	}
	e.ImagePath, e.MetadataJSON = ip.String, mj.String
	return e, true, nil
}

// DeleteFramesBefore removes frames (rows only; caller sweeps dirs) with
// local_date strictly before the cutoff.
func (s *Store) DeleteFramesBefore(cutoffDate string) error {
	_, err := s.db.Exec(`DELETE FROM frames WHERE local_date < ?`, cutoffDate)
	return err
}

// DaysWithStatus lists dates in a given status.
func (s *Store) DaysWithStatus(status string) ([]string, error) {
	rows, err := s.db.Query(`SELECT date FROM days WHERE status=? ORDER BY date`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ForEachFramePath streams every stored frame path (orphan sweep input).
func (s *Store) ForEachFramePath(fn func(path string)) error {
	rows, err := s.db.Query(`SELECT path FROM frames`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return err
		}
		fn(p)
	}
	return rows.Err()
}

// ForEachFrame streams id+path of every frame row (missing-file check).
func (s *Store) ForEachFrame(fn func(id int64, path string)) error {
	rows, err := s.db.Query(`SELECT id, path FROM frames`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			return err
		}
		fn(id, p)
	}
	return rows.Err()
}

// MarkFrameMissing flags one frame row as missing its file.
func (s *Store) MarkFrameMissing(id int64) error {
	_, err := s.db.Exec(`UPDATE frames SET valid='missing_file' WHERE id=?`, id)
	return err
}

// TriggerOverrides parses the per-camera trigger_json onto defaults.
type TriggerOverrides struct {
	Ratio     *float64 `json:"ratio"`
	DeltaAbs  *float64 `json:"delta_abs"`
	RiseDelta *float64 `json:"rise_delta"`
}

func ParseTrigger(overJSON string, def struct {
	Ratio     float64
	DeltaAbs  float64
	RiseDelta float64
}) (ratio, deltaAbs, riseDelta float64, err error) {
	ratio, deltaAbs, riseDelta = def.Ratio, def.DeltaAbs, def.RiseDelta
	if overJSON == "" {
		return
	}
	var o TriggerOverrides
	if err = json.Unmarshal([]byte(overJSON), &o); err != nil {
		return
	}
	if o.Ratio != nil {
		ratio = *o.Ratio
	}
	if o.DeltaAbs != nil {
		deltaAbs = *o.DeltaAbs
	}
	if o.RiseDelta != nil {
		riseDelta = *o.RiseDelta
	}
	return
}

// FrameValidByPath returns the valid flag of the frame at path.
func (s *Store) FrameValidByPath(path string) (string, bool, error) {
	var valid string
	err := s.db.QueryRow(`SELECT valid FROM frames WHERE path=?`, path).Scan(&valid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return valid, true, nil
}

// HasRunningRun reports whether the date has a run still marked running
// (a mid-window crash the tick loop can resume).
func (s *Store) HasRunningRun(localDate string) (bool, error) {
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM capture_runs
	WHERE local_date=? AND mode='production' AND status='running' LIMIT 1`, localDate).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// InsertForecastObservation appends one provider observation (append-only).
func (s *Store) InsertForecastObservation(localDate, provider string, fetchedUTC, eventUTC int64,
	quality float64, detail, rawJSON, algoVersion string, selected bool) error {
	var lead *int64
	if eventUTC > 0 && fetchedUTC > 0 {
		l := (eventUTC - fetchedUTC) / 60000
		lead = &l
	}
	_, err := s.db.Exec(`INSERT INTO forecast_observations(local_date,provider,fetched_utc,event_utc,
	lead_minutes,quality,detail,raw_json,algorithm_version,selected)
	VALUES(?,?,?,?,?,?,?,?,?,?)`, localDate, provider, fetchedUTC, nn(eventUTC), lead,
		quality, nullStr(detail), rawJSON, algoVersion, selected)
	return err
}

// InsertForecastObservationFull also persists the heuristic components JSON.
func (s *Store) InsertForecastObservationFull(localDate, provider string, fetchedUTC, eventUTC int64,
	quality float64, detail, rawJSON, algoVersion string, selected bool, componentsJSON string) error {
	var lead *int64
	if eventUTC > 0 && fetchedUTC > 0 {
		l := (eventUTC - fetchedUTC) / 60000
		lead = &l
	}
	_, err := s.db.Exec(`INSERT INTO forecast_observations(local_date,provider,fetched_utc,event_utc,
lead_minutes,quality,detail,raw_json,algorithm_version,selected,components_json)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`, localDate, provider, fetchedUTC, nn(eventUTC), lead,
		quality, nullStr(detail), rawJSON, algoVersion, selected, nullStr(componentsJSON))
	return err
}

type ForecastObs struct {
	LocalDate  string
	Provider   string
	FetchedUTC int64
	EventUTC   int64
	Quality    float64
	Detail     string
	Selected   bool
	Algorithm  string
	Components string
}

// LatestForecastObservation returns the date's most recent observation for a
// provider.
func (s *Store) LatestForecastObservation(localDate, provider string) (ForecastObs, bool, error) {
	var o ForecastObs
	var detail, comps sql.NullString
	var ev sql.NullInt64
	var sel int
	err := s.db.QueryRow(`SELECT local_date,provider,fetched_utc,event_utc,quality,detail,algorithm_version,selected,components_json
	FROM forecast_observations WHERE local_date=? AND provider=? ORDER BY fetched_utc DESC LIMIT 1`,
		localDate, provider).Scan(&o.LocalDate, &o.Provider, &o.FetchedUTC, &ev, &o.Quality, &detail, &o.Algorithm, &sel, &comps)
	if errors.Is(err, sql.ErrNoRows) {
		return o, false, nil
	}
	if err != nil {
		return o, false, err
	}
	o.EventUTC, o.Detail, o.Selected, o.Components = ev.Int64, detail.String, sel == 1, comps.String
	return o, true, nil
}

// ForecastComparison returns per-date latest quality for both providers for
// the last N days, joined with observed best color score.
func (s *Store) ForecastComparison(days int) ([]map[string]any, error) {
	rows, err := s.db.Query(`SELECT local_date, provider, quality FROM forecast_observations
	WHERE fetched_utc = (SELECT MAX(fetched_utc) FROM forecast_observations f2
	WHERE f2.local_date = forecast_observations.local_date AND f2.provider = forecast_observations.provider)
	ORDER BY local_date DESC LIMIT ?`, days*4)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byDate := map[string]map[string]any{}
	var order []string
	for rows.Next() {
		var d, p string
		var q float64
		if err := rows.Scan(&d, &p, &q); err != nil {
			return nil, err
		}
		if _, ok := byDate[d]; !ok {
			byDate[d] = map[string]any{"date": d}
			order = append(order, d)
		}
		byDate[d][p] = q
	}
	if len(order) > days {
		order = order[:days]
	}
	var out []map[string]any
	for _, d := range order {
		row := byDate[d]
		var bs sql.NullFloat64
		if err := s.db.QueryRow(`SELECT best_score FROM days WHERE date=?`, d).Scan(&bs); err == nil && bs.Valid {
			row["observed"] = bs.Float64
		}
		out = append(out, row)
	}
	return out, nil
}

// PendingDeliveries lists deliveries needing work.
func (s *Store) PendingDeliveries() ([]struct {
	EventID, NotifierID string
	Attempts            int
}, error) {
	rows, err := s.db.Query(`SELECT event_id, notifier_id, attempts FROM alert_deliveries
	WHERE state IN ('pending','sending') ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []struct {
		EventID, NotifierID string
		Attempts            int
	}
	for rows.Next() {
		var d struct {
			EventID, NotifierID string
			Attempts            int
		}
		if err := rows.Scan(&d.EventID, &d.NotifierID, &d.Attempts); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetEvent fetches an alert event row by id.
func (s *Store) GetEvent(id string) (AlertEvent, bool, error) {
	var e AlertEvent
	var ip, mj sql.NullString
	err := s.db.QueryRow(`SELECT id,event_key,local_date,kind,title,body,image_path,metadata_json,created_utc
	FROM alert_events WHERE id=?`, id).
		Scan(&e.ID, &e.EventKey, &e.LocalDate, &e.Kind, &e.Title, &e.Body, &ip, &mj, &e.CreatedUTC)
	if errors.Is(err, sql.ErrNoRows) {
		return e, false, nil
	}
	if err != nil {
		return e, false, err
	}
	e.ImagePath, e.MetadataJSON = ip.String, mj.String
	return e, true, nil
}

// MarkDelivery records a delivery outcome.
func (s *Store) MarkDelivery(eventID, notifierID, state, lastErr string) error {
	if lastErr == "" {
		_, err := s.db.Exec(`UPDATE alert_deliveries SET state=?, attempts=attempts+1,
		sent_utc=CASE WHEN ?='sent' THEN ? ELSE sent_utc END WHERE event_id=? AND notifier_id=?`,
			state, state, time.Now().UnixMilli(), eventID, notifierID)
		return err
	}
	_, err := s.db.Exec(`UPDATE alert_deliveries SET state=?, attempts=attempts+1, last_error=?
	WHERE event_id=? AND notifier_id=?`, state, lastErr, eventID, notifierID)
	return err
}

// RecentEvents lists the newest alert events with per-notifier delivery states.
func (s *Store) RecentEvents(limit int) ([]AlertEvent, error) {
	rows, err := s.db.Query(`SELECT id,event_key,local_date,kind,title,body,image_path,metadata_json,created_utc
	FROM alert_events ORDER BY created_utc DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertEvent
	for rows.Next() {
		var e AlertEvent
		var ip, mj sql.NullString
		if err := rows.Scan(&e.ID, &e.EventKey, &e.LocalDate, &e.Kind, &e.Title, &e.Body, &ip, &mj, &e.CreatedUTC); err != nil {
			return nil, err
		}
		e.ImagePath, e.MetadataJSON = ip.String, mj.String
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeliveriesForEvent returns the delivery rows of one event.
func (s *Store) DeliveriesForEvent(eventID string) ([]struct {
	NotifierID, State, LastErr string
	Attempts                   int
}, error) {
	rows, err := s.db.Query(`SELECT notifier_id,state,COALESCE(last_error,''),attempts
	FROM alert_deliveries WHERE event_id=?`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []struct {
		NotifierID, State, LastErr string
		Attempts                   int
	}
	for rows.Next() {
		var d struct {
			NotifierID, State, LastErr string
			Attempts                   int
		}
		if err := rows.Scan(&d.NotifierID, &d.State, &d.LastErr, &d.Attempts); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// EnabledNotifierIDs returns the static-def notifiers enabled at runtime.
// Static defs live in config; DB stores enablement overrides.
func (s *Store) EnabledNotifierIDs(defs []string) []string {
	m := map[string]bool{}
	if ok, _ := s.GetSettingRaw("notifier_enabled", &m); !ok {
		m = nil
	}
	var out []string
	for _, id := range defs {
		if m == nil || m[id] {
			out = append(out, id)
		}
	}
	return out
}

// LatestDays returns completed days newest-first for the gallery.
func (s *Store) LatestDays(limit, offset int) ([]Day, error) {
	rows, err := s.db.Query(`SELECT date,status,COALESCE(reason,''),best_score,COALESCE(best_camera_id,''),
	COALESCE(best_taken_utc,0),COALESCE(best_path,''),COALESCE(thumb480_path,''),COALESCE(thumb240_path,''),COALESCE(completed_utc,0)
	FROM days WHERE date <= date('now','localtime') ORDER BY date DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Day
	for rows.Next() {
		var d Day
		var bs sql.NullFloat64
		if err := rows.Scan(&d.Date, &d.Status, &d.Reason, &bs, &d.BestCameraID, &d.BestTakenUTC,
			&d.BestPath, &d.Thumb480Path, &d.Thumb240Path, &d.CompletedUTC); err != nil {
			return nil, err
		}
		if bs.Valid {
			v := bs.Float64
			d.BestScore = &v
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// CountDays counts gallery rows.
func (s *Store) CountDays() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM days WHERE date <= date('now','localtime')`).Scan(&n)
	return n, err
}

// NYCTMCCacheEntry is one cached DOT camera.
type NYCTMCCacheEntry struct {
	DotID    string
	Name     string
	Lat, Lon float64
	Online   bool
}

// UpsertNYCTMCList replaces the cached DOT camera list.
func (s *Store) UpsertNYCTMCList(entries []NYCTMCCacheEntry) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	tx.Exec(`DELETE FROM nyctmc_cameras`)
	now := time.Now().UnixMilli()
	for _, e := range entries {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO nyctmc_cameras(dot_id,name,lat,lon,online,refreshed_utc)
			VALUES(?,?,?,?,?,?)`, e.DotID, e.Name, e.Lat, e.Lon, e.Online, now); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// ListNYCTMCNearest returns cached DOT cameras sorted by distance from
// lat/lon (nearest first).
func (s *Store) ListNYCTMCNearest(lat, lon float64, limit int) ([]NYCTMCCacheEntry, error) {
	rows, err := s.db.Query(`SELECT dot_id,name,lat,lon,COALESCE(online,0) FROM nyctmc_cameras
	ORDER BY (lat-?)*(lat-?) + (lon-?)*(lon-?) ASC LIMIT ?`, lat, lat, lon, lon, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NYCTMCCacheEntry
	for rows.Next() {
		var e NYCTMCCacheEntry
		var on int
		if err := rows.Scan(&e.DotID, &e.Name, &e.Lat, &e.Lon, &on); err != nil {
			return nil, err
		}
		e.Online = on == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// FrameLabel is one operator ground-truth tag with snapshotted diagnostics.
type FrameLabel struct {
	ID                                              int64
	CameraID                                        string
	Kind                                            string
	TaggedUTC                                       int64
	LocalDate                                       string
	Score, SunsetPixelFraction, MedianL, MeanChroma float64
	ScoringVersion                                  string
	Notes                                           string
}

// InsertFrameLabel records a tag (score fields snapshotted by the caller).
func (s *Store) InsertFrameLabel(l *FrameLabel) error {
	_, err := s.db.Exec(`INSERT INTO frame_labels(camera_id,kind,tagged_utc,local_date,
	score,sunset_pixel_fraction,median_l,mean_chroma,scoring_version,notes)
	VALUES(?,?,?,?,?,?,?,?,?,?)`, l.CameraID, l.Kind, l.TaggedUTC, l.LocalDate,
		l.Score, l.SunsetPixelFraction, l.MedianL, l.MeanChroma,
		l.ScoringVersion, nullStr(l.Notes))
	return err
}

// LabelBaseline aggregates a camera's sunset vs not-sunset tag averages.
type LabelBaseline struct {
	CameraID     string
	SunsetN      int
	SunsetAvg    float64
	NotSunsetN   int
	NotSunsetAvg float64
}

// LabelBaselines aggregates per camera: avg score for each kind and counts.
// This is the sunset / not-sunset separation signal for tuning.
func (s *Store) LabelBaselines() ([]LabelBaseline, error) {
	rows, err := s.db.Query(`SELECT camera_id, kind, COUNT(*), AVG(score)
	FROM frame_labels GROUP BY camera_id, kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type agg struct {
		CameraID     string
		SunsetN      int
		SunsetAvg    float64
		NotSunsetN   int
		NotSunsetAvg float64
	}
	m := map[string]*agg{}
	var order []string
	for rows.Next() {
		var cam, kind string
		var n int
		var avg float64
		if err := rows.Scan(&cam, &kind, &n, &avg); err != nil {
			return nil, err
		}
		a, ok := m[cam]
		if !ok {
			a = &agg{CameraID: cam}
			m[cam] = a
			order = append(order, cam)
		}
		if kind == "sunset" {
			a.SunsetN, a.SunsetAvg = n, avg
		} else {
			a.NotSunsetN, a.NotSunsetAvg = n, avg
		}
	}
	out := make([]LabelBaseline, 0, len(order))
	for _, cam := range order {
		a := m[cam]
		out = append(out, LabelBaseline{a.CameraID, a.SunsetN, a.SunsetAvg, a.NotSunsetN, a.NotSunsetAvg})
	}
	return out, rows.Err()
}

// RecentLabels returns the newest tags for the review page.
func (s *Store) RecentLabels(limit int) ([]FrameLabel, error) {
	rows, err := s.db.Query(`SELECT id,camera_id,kind,tagged_utc,local_date,
	COALESCE(score,0),COALESCE(sunset_pixel_fraction,0),COALESCE(median_l,0),
	COALESCE(mean_chroma,0),COALESCE(scoring_version,''),COALESCE(notes,'')
	FROM frame_labels ORDER BY tagged_utc DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FrameLabel
	for rows.Next() {
		var l FrameLabel
		if err := rows.Scan(&l.ID, &l.CameraID, &l.Kind, &l.TaggedUTC, &l.LocalDate,
			&l.Score, &l.SunsetPixelFraction, &l.MedianL, &l.MeanChroma,
			&l.ScoringVersion, &l.Notes); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// CapturedFrame is one row of a capture sequence for the frames viewer.
type CapturedFrame struct {
	ID         int64
	CameraID   string
	FetchedUTC int64
	Score      *float64
	MedianL    *float64
	Valid      string
	Path       string
}

// FramesForDate returns the day's captured frames newest-last.
func (s *Store) FramesForDate(localDate string) ([]CapturedFrame, error) {
	rows, err := s.db.Query(`SELECT id,camera_id,fetched_utc,score,median_l,valid,path
	FROM frames WHERE local_date=? ORDER BY fetched_utc`, localDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CapturedFrame
	for rows.Next() {
		var f CapturedFrame
		var sc, ml sql.NullFloat64
		if err := rows.Scan(&f.ID, &f.CameraID, &f.FetchedUTC, &sc, &ml, &f.Valid, &f.Path); err != nil {
			return nil, err
		}
		if sc.Valid {
			v := sc.Float64
			f.Score = &v
		}
		if ml.Valid {
			v := ml.Float64
			f.MedianL = &v
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FramePathByID returns the stored path for an id (viewer image serving).
func (s *Store) FramePathByID(id int64) (string, bool, error) {
	var p string
	err := s.db.QueryRow(`SELECT path FROM frames WHERE id=?`, id).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return p, true, nil
}

// FramePathsForCamera lists stored frame file paths (for cleanup on delete).
func (s *Store) FramePathsForCamera(cameraID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT path FROM frames WHERE camera_id=?`, cameraID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteCameraCascade removes a camera plus its frames and labels, and
// clears days references, in one transaction. Frame files are the caller's
// to remove (store has no data root).
func (s *Store) DeleteCameraCascade(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM frames WHERE camera_id=?`,
		`DELETE FROM frame_labels WHERE camera_id=?`,
		`UPDATE days SET best_camera_id=NULL WHERE best_camera_id=?`,
		`DELETE FROM cameras WHERE id=?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}
