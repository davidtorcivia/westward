// Package webadmin serves the authenticated admin backend: cameras,
// ROI/crop editors, alerts, forecasts, settings, status.
package webadmin

import (
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/davidtorcivia/westward/internal/config"
	"github.com/davidtorcivia/westward/internal/engine"
	"github.com/davidtorcivia/westward/internal/server"
	"github.com/davidtorcivia/westward/internal/source"
	"github.com/davidtorcivia/westward/internal/store"
	"github.com/davidtorcivia/westward/internal/ulid"
)

//go:embed templates/*.html static/*
var content embed.FS

type Admin struct {
	Store  *store.Store
	Engine *engine.Engine
	Alerts *engine.AlertManager
	Auth   *server.Auth
	Log    *slog.Logger
	// Preview fetches one frame from a camera (nil = disabled).
	Preview func(cam store.Camera) ([]byte, int, int, error)
	// SunsetToday returns today's solar events (nil = not computed).
	SunsetToday func() (time.Time, time.Time, bool)
}

func New(st *store.Store, e *engine.Engine, a *engine.AlertManager, auth *server.Auth, log *slog.Logger) (*Admin, error) {
	if log == nil {
		log = slog.Default()
	}
	ad := &Admin{Store: st, Engine: e, Alerts: a, Auth: auth, Log: log}
	return ad, nil
}

var funcs = template.FuncMap{
	"roiJSON":  func(c store.Camera) string { return rectJSON(c.ROIX, c.ROIY, c.ROIW, c.ROIH) },
	"cropJSON": func(c store.Camera) string { return rectJSON(c.CropX, c.CropY, c.CropW, c.CropH) },
	"triggerOf": func(c store.Camera) map[string]float64 {
		v := map[string]float64{"Threshold": c.ThresholdAbs, "Ratio": 1.6, "Delta": 4.0, "Rise": 1.5}
		if c.TriggerJSON != "" {
			var o map[string]float64
			if json.Unmarshal([]byte(c.TriggerJSON), &o) == nil {
				if r, ok := o["ratio"]; ok {
					v["Ratio"] = r
				}
				if d, ok := o["delta_abs"]; ok {
					v["Delta"] = d
				}
				if rd, ok := o["rise_delta"]; ok {
					v["Rise"] = rd
				}
			}
		}
		return v
	},
	"printDur": func(secs int) time.Duration { return time.Duration(secs) * time.Second },
}

func rectJSON(x, y, w, h *float64) string {
	if x == nil || y == nil || w == nil || h == nil {
		return ""
	}
	b, _ := json.Marshal([4]float64{*x, *y, *w, *h})
	return string(b)
}

func (a *Admin) tpl(names ...string) (*template.Template, error) {
	files := make([]string, 0, len(names)+1)
	files = append(files, "templates/layout.html")
	for _, n := range names {
		files = append(files, "templates/"+n+".html")
	}
	return template.New("layout.html").Funcs(funcs).ParseFS(content, files...)
}

func (a *Admin) render(w http.ResponseWriter, r *http.Request, names []string, data any) {
	t, err := a.tpl(names...)
	if err != nil {
		http.Error(w, "template: "+err.Error(), http.StatusInternalServerError)
		return
	}
	token := server.EnsureCSRFCookie(w, r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page := names[len(names)-1] + ".html"
	if err := t.ExecuteTemplate(w, page, map[string]any{"Data": data, "CSRF": token}); err != nil {
		a_log(r, "render: "+page+": "+err.Error())
	}
}

func a_log(*http.Request, string) {}

// Register wires all admin routes onto the server's guarded admin mux.
func (a *Admin) Register(s *server.Server) {
	// Public (unauthenticated) auth routes.
	s.Mux.HandleFunc("GET /admin/login", a.LoginHandler())
	s.Mux.HandleFunc("POST /admin/login", a.LoginHandler())

	s.Admin("POST /admin/logout", a.LogoutHandler())
	s.Admin("POST /admin/password", a.passwordChange)
	s.Admin("GET /admin", a.dashboard)
	s.Admin("GET /admin/cameras", a.cameras)
	s.Admin("POST /admin/cameras/save", a.cameraSave)
	s.Admin("POST /admin/cameras/delete", a.cameraDelete)
	s.Admin("POST /admin/cameras/preview", a.cameraPreview)
	s.Admin("GET /admin/alerts", a.alerts)
	s.Admin("POST /admin/alerts/test", a.alertTest)
	s.Admin("GET /admin/forecast", a.forecast)
	s.Admin("GET /admin/settings", a.settings)
	s.Admin("POST /admin/settings/save", a.settingsSave)
	s.Admin("GET /admin/status", a.status)
	s.Admin("POST /admin/recrop", a.recrop)
	staticHandler := http.StripPrefix("/admin/static/", http.FileServer(http.FS(mustSub())))
	s.Admin("GET /admin/static/", func(w http.ResponseWriter, r *http.Request) {
		staticHandler.ServeHTTP(w, r)
	})
	s.Admin("GET /admin/map", a.mapPage)
	s.Admin("GET /admin/map/data", a.mapData)
	s.Admin("POST /admin/dot/refresh", a.dotRefresh)
	s.Admin("GET /admin/notifiers", a.notifiers)
	s.Admin("POST /admin/notifiers/save", a.notifierSave)
	s.Admin("POST /admin/notifiers/delete", a.notifierDelete)
	s.Admin("GET /admin/cameras/shot/{id}", a.cameraShot)
}

// cameraShot serves a preview JPEG as an <img> target (GET, session-auth).
func (a *Admin) cameraShot(w http.ResponseWriter, r *http.Request) {
	if a.Preview == nil {
		http.Error(w, "preview unavailable", http.StatusServiceUnavailable)
		return
	}
	cam, err := a.Store.GetCamera(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	jpegBytes, _, _, err := a.Preview(cam)
	if err != nil {
		http.Error(w, "fetch failed", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(jpegBytes)
}

// dotRefresh pulls the DOT camera list into the cache.
func (a *Admin) dotRefresh(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	list, err := source.FetchNYCTMCList(ctx, source.NYCTMCBase, source.NYCTMCUserAgent)
	if err != nil {
		http.Error(w, "DOT list fetch failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	var entries []store.NYCTMCCacheEntry
	for _, c := range list {
		entries = append(entries, store.NYCTMCCacheEntry{
			DotID: c.ID, Name: c.Name, Lat: c.Latitude, Lon: c.Longitude,
			Online: c.IsOnline == "true",
		})
	}
	if err := a.Store.UpsertNYCTMCList(entries); err != nil {
		http.Error(w, "store failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/map", http.StatusSeeOther)
}

// mapData returns cameras + nearest cached DOT cameras as JSON.
func (a *Admin) mapData(w http.ResponseWriter, r *http.Request) {
	settings, _, _ := a.Store.GetSettings()
	cams, _ := a.Store.ListCameras()
	dot, _ := a.Store.ListNYCTMCNearest(settings.Lat, settings.Lon, 2000)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"home":    map[string]float64{"lat": settings.Lat, "lon": settings.Lon},
		"cameras": cams, "dot": dot,
	})
}

func (a *Admin) mapPage(w http.ResponseWriter, r *http.Request) {
	settings, _, _ := a.Store.GetSettings()
	// Template reads .lat/.lon for the initial map view.
	a.render(w, r, []string{"map"}, map[string]any{
		"lat": settings.Lat, "lon": settings.Lon,
	})
}

func (a *Admin) notifiers(w http.ResponseWriter, r *http.Request) {
	defs, _ := engine.LoadNotifierDefs(a.Store, config.Static{})
	a.render(w, r, []string{"notifiers"}, map[string]any{"Defs": defs, "Saved": r.URL.Query().Get("saved")})
}

func (a *Admin) notifierSave(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	defs, _ := engine.LoadNotifierDefs(a.Store, config.Static{})
	id := f.Get("id")
	d := engine.NotifierDef{
		ID: id, Type: f.Get("type"), Enabled: f.Get("enabled") == "on",
		Server: strings.TrimSpace(f.Get("server")), Topic: strings.TrimSpace(f.Get("topic")),
		TokenEnv: strings.TrimSpace(f.Get("token_env")), UserEnv: strings.TrimSpace(f.Get("user_env")),
		URL: strings.TrimSpace(f.Get("url")), HMACEnv: strings.TrimSpace(f.Get("hmac_env")),
	}
	if d.ID == "" {
		d.ID = ulid.New(time.Now())
	}
	switch d.Type {
	case "ntfy", "pushover", "webhook":
	default:
		http.Error(w, "unknown type", http.StatusBadRequest)
		return
	}
	replaced := false
	for i := range defs {
		if defs[i].ID == d.ID {
			defs[i] = d
			replaced = true
		}
	}
	if !replaced {
		defs = append(defs, d)
	}
	if err := a.Store.SetSettingRaw("notifiers", defs); err != nil {
		http.Error(w, "store: "+err.Error(), http.StatusInternalServerError)
		return
	}
	a.reloadNotifiers(defs)
	http.Redirect(w, r, "/admin/notifiers?saved=1", http.StatusSeeOther)
}

func (a *Admin) notifierDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PostFormValue("id")
	defs, _ := engine.LoadNotifierDefs(a.Store, config.Static{})
	var out []engine.NotifierDef
	for _, d := range defs {
		if d.ID != id {
			out = append(out, d)
		}
	}
	a.Store.SetSettingRaw("notifiers", out)
	a.reloadNotifiers(out)
	http.Redirect(w, r, "/admin/notifiers", http.StatusSeeOther)
}

func (a *Admin) reloadNotifiers(defs []engine.NotifierDef) {
	if a.Alerts != nil {
		a.Alerts.Reload(defs)
	}
}

func mustSub() fs.FS {
	sub, err := fs.Sub(content, "static")
	if err != nil {
		panic(err)
	}
	return sub
}

func (a *Admin) dashboard(w http.ResponseWriter, r *http.Request) {
	settings, _, _ := a.Store.GetSettings()
	var sunset, dusk time.Time
	var hasSun bool
	if a.SunsetToday != nil {
		sunset, dusk, hasSun = a.SunsetToday()
	}
	today := time.Now().Format("2006-01-02")
	om, omOK, _ := a.Store.LatestForecastObservation(today, "openmeteo")
	sh, shOK, _ := a.Store.LatestForecastObservation(today, "sunsethue")
	goEv, _, _ := a.Store.GetEventByKey(today + ":go")
	cams, _ := a.Store.ListCameras()
	days, _ := a.Store.LatestDays(7, 0)
	data := map[string]any{
		"Settings": settings, "HasSun": hasSun,
		"Sunset": sunset, "Dusk": dusk,
		"OM": om, "OMOK": omOK, "SH": sh, "SHOK": shOK,
		"GO": goEv, "Cameras": cams, "Days": days,
	}
	a.render(w, r, []string{"dashboard"}, data)
}

func (a *Admin) cameras(w http.ResponseWriter, r *http.Request) {
	cams, err := a.Store.ListCameras()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.render(w, r, []string{"cameras"}, map[string]any{"Cameras": cams, "Saved": r.URL.Query().Get("saved")})
}

func (a *Admin) cameraSave(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	id := f.Get("id")
	create := id == ""
	cam := store.Camera{
		ID: id, Name: strings.TrimSpace(f.Get("name")),
		Type: f.Get("type"), Ref: strings.TrimSpace(f.Get("ref")),
		Enabled:       f.Get("enabled") == "on",
		Role:          valueOr(f.Get("role"), "trigger_only"),
		Attribution:   strings.TrimSpace(f.Get("attribution")),
		CredentialRef: strings.TrimSpace(f.Get("credential_ref")),
		State:         "ok",
	}
	cam.PublishPriority, _ = strconv.Atoi(valueOr(f.Get("publish_priority"), "0"))
	if f.Get("publish_eligible") == "on" {
		cam.PublishEligible = true
	}
	cam.ThresholdAbs = floatOf(valueOr(f.Get("threshold_abs"), "12"), 12)
	if s := f.Get("roi"); s != "" {
		var roi [4]float64
		if err := json.Unmarshal([]byte(s), &roi); err == nil {
			cam.ROIX, cam.ROIY, cam.ROIW, cam.ROIH = &roi[0], &roi[1], &roi[2], &roi[3]
		}
	}
	if s := f.Get("crop"); s != "" {
		var c [4]float64
		if err := json.Unmarshal([]byte(s), &c); err == nil {
			cam.CropX, cam.CropY, cam.CropW, cam.CropH = &c[0], &c[1], &c[2], &c[3]
		}
	}
	if v := floatPtr(f.Get("lat")); v != nil {
		cam.Lat = v
	}
	if v := floatPtr(f.Get("lon")); v != nil {
		cam.Lon = v
	}
	// nyctmc: coordinates from the DOT cache when present (map picker sends them).
	if cam.Type == "nyctmc" && (cam.Lat == nil || cam.Lon == nil) {
		if entries, err := a.Store.ListNYCTMCNearest(40.6782, -73.9442, 2000); err == nil {
			for _, e := range entries {
				if e.DotID == cam.Ref {
					lat, lon := e.Lat, e.Lon
					cam.Lat, cam.Lon = &lat, &lon
					break
				}
			}
		}
	}
	if tj := triggerJSON(f); tj != "" {
		cam.TriggerJSON = tj
	}
	var err error
	if create {
		err = a.Store.InsertCamera(&cam)
	} else {
		err = a.Store.UpdateCamera(&cam)
	}
	if err != nil {
		http.Error(w, "save: "+err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/cameras?saved=1", http.StatusSeeOther)
}

func triggerJSON(f url.Values) string {
	m := map[string]float64{}
	for k, def := range map[string]float64{"ratio": 1.6, "delta_abs": 4.0, "rise_delta": 1.5} {
		if v := f.Get(k); v != "" {
			if x, err := strconv.ParseFloat(v, 64); err == nil && x != def {
				m[k] = x
			}
		}
	}
	if len(m) == 0 {
		return ""
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func (a *Admin) cameraDelete(w http.ResponseWriter, r *http.Request) {
	a.Store.DeleteCamera(r.PostFormValue("id"))
	http.Redirect(w, r, "/admin/cameras", http.StatusSeeOther)
}

func (a *Admin) cameraPreview(w http.ResponseWriter, r *http.Request) {
	if a.Preview == nil {
		http.Error(w, "preview unavailable", http.StatusServiceUnavailable)
		return
	}
	cam, err := a.Store.GetCamera(r.PostFormValue("id"))
	if err != nil {
		http.Error(w, "camera not found", http.StatusNotFound)
		return
	}
	jpegBytes, wd, ht, err := a.Preview(cam)
	if err != nil {
		http.Error(w, "fetch failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(jpegBytes)
	_ = wd
	_ = ht
}

func (a *Admin) alerts(w http.ResponseWriter, r *http.Request) {
	events, _ := a.Store.RecentEvents(50)
	type evRow struct {
		store.AlertEvent
		Deliveries []struct {
			NotifierID, State, LastErr string
			Attempts                   int
		}
	}
	rows := make([]evRow, 0, len(events))
	for _, e := range events {
		d, _ := a.Store.DeliveriesForEvent(e.ID)
		rows = append(rows, evRow{AlertEvent: e, Deliveries: d})
	}
	a.render(w, r, []string{"alerts"}, map[string]any{"Events": rows})
}

func (a *Admin) alertTest(w http.ResponseWriter, r *http.Request) {
	if a.Alerts == nil {
		http.Error(w, "notifiers not configured", http.StatusServiceUnavailable)
		return
	}
	ok, err := a.Alerts.TestFire(r.Context())
	msg := "sent"
	if err != nil {
		msg = "error: " + err.Error()
	} else if !ok {
		msg = "duplicate (already fired)"
	}
	http.Redirect(w, r, "/admin/alerts?test="+msg, http.StatusSeeOther)
}

func (a *Admin) forecast(w http.ResponseWriter, r *http.Request) {
	cmp, _ := a.Store.ForecastComparison(30)
	settings, _, _ := a.Store.GetSettings()
	a.render(w, r, []string{"forecast"}, map[string]any{"Cmp": cmp, "Settings": settings})
}

func (a *Admin) settings(w http.ResponseWriter, r *http.Request) {
	settings, _, _ := a.Store.GetSettings()
	a.render(w, r, []string{"settings"}, map[string]any{"Settings": settings, "Saved": r.URL.Query().Get("saved")})
}

func (a *Admin) settingsSave(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	settings, _, err := a.Store.GetSettings()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	settings.Lat = floatOf(f.Get("lat"), settings.Lat)
	settings.Lon = floatOf(f.Get("lon"), settings.Lon)
	settings.TZ = valueOr(f.Get("tz"), settings.TZ)
	settings.Capture.IntervalS = intOf(f.Get("interval_s"), settings.Capture.IntervalS)
	settings.Capture.BeforeS = intOf(f.Get("before_s"), settings.Capture.BeforeS)
	settings.Capture.AfterS = intOf(f.Get("after_s"), settings.Capture.AfterS)
	settings.RetentionFramesDays = intOf(f.Get("retention_days"), settings.RetentionFramesDays)
	settings.QualityFloor = floatOf(f.Get("quality_floor"), settings.QualityFloor)
	settings.Archive.DarknessFloor = floatOf(f.Get("darkness_floor"), settings.Archive.DarknessFloor)
	settings.Archive.CutoffAfterDuskS = intOf(f.Get("cutoff_s"), settings.Archive.CutoffAfterDuskS)
	settings.Trigger.ThresholdAbs = floatOf(f.Get("trigger_abs"), settings.Trigger.ThresholdAbs)
	settings.Trigger.Ratio = floatOf(f.Get("trigger_ratio"), settings.Trigger.Ratio)
	settings.Trigger.DeltaAbs = floatOf(f.Get("trigger_delta"), settings.Trigger.DeltaAbs)
	settings.Trigger.RiseDelta = floatOf(f.Get("trigger_rise"), settings.Trigger.RiseDelta)
	settings.Forecast.Provider = valueOr(f.Get("provider"), settings.Forecast.Provider)
	if _, err := a.Store.SaveSettings(settings); err != nil {
		http.Error(w, "save: "+err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/settings?saved=1", http.StatusSeeOther)
}

func (a *Admin) status(w http.ResponseWriter, r *http.Request) {
	settings, rev, _ := a.Store.GetSettings()
	pending, _ := a.Store.PendingDeliveries()
	cams, _ := a.Store.ListCameras()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"config_revision": rev,
		"tz":              settings.TZ, "lat": settings.Lat, "lon": settings.Lon,
		"cameras": len(cams), "pending_deliveries": len(pending),
		"ok": true,
	})
}

func (a *Admin) recrop(w http.ResponseWriter, r *http.Request) {
	date := r.PostFormValue("date")
	if a.Engine == nil {
		http.Error(w, "engine not running", http.StatusServiceUnavailable)
		return
	}
	if err := a.Engine.RecropDay(r.Context(), date); err != nil {
		http.Error(w, "recrop: "+err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func valueOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func floatPtr(s string) *float64 {
	if s == "" {
		return nil
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return &v
	}
	return nil
}

func floatOf(s string, def float64) float64 {
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v
	}
	return def
}

func intOf(s string, def int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return def
}

// LoginHandler serves the login page (GET) and verifies credentials (POST).
// Unauthenticated by design; rate-limited via the shared Auth limiter.
func (a *Admin) LoginHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			ip, _, _ := net.SplitHostPort(r.RemoteAddr)
			if a.Auth.TooManyFailures(ip) {
				http.Error(w, "too many attempts, wait a minute", http.StatusTooManyRequests)
				return
			}
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			if !a.Auth.Check(r.PostFormValue("username"), r.PostFormValue("password")) {
				a.Auth.RecordFailure(ip)
				a.renderLogin(w, "wrong username or password")
				return
			}
			tok := a.Auth.Sessions.Create()
			http.SetCookie(w, &http.Cookie{
				Name: "westward_session", Value: tok, Path: "/admin",
				MaxAge: 86400, HttpOnly: true, SameSite: http.SameSiteLaxMode,
			})
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return
		}
		a.renderLogin(w, "")
	}
}

func (a *Admin) renderLogin(w http.ResponseWriter, errMsg string) {
	t, err := template.ParseFS(content, "templates/login.html")
	if err != nil {
		http.Error(w, "template: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	t.Execute(w, map[string]any{"Error": errMsg})
}

// LogoutHandler revokes the session.
func (a *Admin) LogoutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("westward_session"); err == nil {
			a.Auth.Sessions.Revoke(c.Value)
		}
		http.SetCookie(w, &http.Cookie{Name: "westward_session", Value: "", Path: "/admin", MaxAge: -1})
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
	}
}

// passwordChange stores the new scrypt hash in DB settings, superseding the
// env bootstrap password from the next check onward.
func (a *Admin) passwordChange(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	cur, next := f.Get("current"), f.Get("next")
	if len(next) < 12 {
		http.Error(w, "new password must be at least 12 characters", http.StatusBadRequest)
		return
	}
	if !a.Auth.Check(a.Auth.User, cur) {
		http.Error(w, "current password wrong", http.StatusForbidden)
		return
	}
	hash, err := server.HashPassword(next)
	if err != nil {
		http.Error(w, "hash: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := a.Store.SetSettingRaw("admin_pw", hash); err != nil {
		http.Error(w, "store: "+err.Error(), http.StatusInternalServerError)
		return
	}
	a.HookAuth() // rebind Verify immediately
	a.Log.Info("admin password changed")
	http.Redirect(w, r, "/admin/settings?saved=password", http.StatusSeeOther)
}

// hookAuth binds Auth.Verify to the DB-stored scrypt hash when present.
func (a *Admin) HookAuth() {
	stored := ""
	if ok, _ := a.Store.GetSettingRaw("admin_pw", &stored); ok && stored != "" {
		a.Auth.Verify = func(pw string) bool { return server.VerifyPassword(pw, stored) }
	} else {
		a.Auth.Verify = nil
	}
}
