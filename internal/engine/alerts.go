package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/davidtorcivia/westward/internal/config"
	"github.com/davidtorcivia/westward/internal/notify"
	"github.com/davidtorcivia/westward/internal/store"
	"github.com/davidtorcivia/westward/internal/ulid"
	"golang.org/x/image/draw"
)

// AlertManager owns durable alert delivery: events are inserted with pending
// deliveries before any network I/O; a worker fans out, retries transient
// failures with backoff, and never retries permanent ones.
type AlertManager struct {
	Store     *store.Store
	Log       *slog.Logger
	Notifiers map[string]notify.Notifier
	IDs       []string // enabled notifier ids in definition order

	mu sync.Mutex
}

func (m *AlertManager) notifierIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.IDs
}

// Emit creates an event + one pending delivery per enabled notifier,
// atomically, before any network I/O.
func (m *AlertManager) Emit(e *store.AlertEvent) (bool, error) {
	return m.Store.TryInsertEvent(e, m.notifierIDs())
}

// Worker processes pending deliveries until ctx is done. Tick 5 s; transient
// failures retry up to 3 attempts with 5s/20s/80s backoff; permanent errors
// fail immediately.
func (m *AlertManager) Worker(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Drain(ctx)
		}
	}
}

func (m *AlertManager) Drain(ctx context.Context) {
	pending, err := m.Store.PendingDeliveries()
	if err != nil {
		m.Log.Error("delivery queue read failed", "err", err.Error())
		return
	}
	for _, d := range pending {
		n, ok := m.Notifiers[d.NotifierID]
		if !ok {
			m.Store.MarkDelivery(d.EventID, d.NotifierID, "failed", "notifier not configured")
			continue
		}
		ev, ok, err := m.Store.GetEvent(d.EventID)
		if err != nil || !ok {
			continue
		}
		var image []byte
		if ev.ImagePath != "" {
			image, _ = os.ReadFile(ev.ImagePath)
		}
		alert := notify.Alert{
			EventID: ev.ID, Kind: ev.Kind, Title: ev.Title, Body: ev.Body,
			ImageJPEG: image, Priority: notify.PriorityNormal,
		}
		if ev.Kind == "go" {
			alert.Priority = notify.PriorityHigh
		}
		err = n.Send(ctx, alert)
		var te *notify.TransientError
		var pe *notify.PermanentError
		switch {
		case err == nil:
			m.Store.MarkDelivery(d.EventID, d.NotifierID, "sent", "")
			m.Log.Info("alert delivered", "notifier", d.NotifierID, "kind", ev.Kind)
		case errors.As(err, &pe):
			m.Store.MarkDelivery(d.EventID, d.NotifierID, "failed", pe.Error())
			m.Log.Warn("alert permanently failed", "notifier", d.NotifierID, "err", pe.Error())
		case errors.As(err, &te):
			if d.Attempts+1 >= 3 {
				m.Store.MarkDelivery(d.EventID, d.NotifierID, "failed", "retries exhausted: "+te.Error())
			} else {
				m.Store.MarkDelivery(d.EventID, d.NotifierID, "pending", te.Error())
				m.Log.Warn("alert transiently failed, will retry",
					"notifier", d.NotifierID, "attempt", d.Attempts+1, "err", te.Error())
			}
		default:
			m.Store.MarkDelivery(d.EventID, d.NotifierID, "failed", err.Error())
		}
	}
}

// HeadsUp evaluates the forecast floor and emits the heads-up event.
func (m *AlertManager) HeadsUp(ctx context.Context, date string, quality float64, floor float64,
	sunset time.Time, detail, provider string) error {
	if quality < floor {
		m.Log.Info("heads-up below floor, skipped", "quality", quality, "floor", floor)
		return nil
	}
	title := fmt.Sprintf("Sunset %s — quality %.0f/100", sunset.Format("15:04"), quality)
	body := fmt.Sprintf("%s forecast: %s.", provider, detail)
	ok, err := m.Emit(&store.AlertEvent{
		EventKey: date + ":headsup", LocalDate: date, Kind: "headsup",
		Title: title, Body: body,
	})
	if err != nil {
		return err
	}
	if ok {
		m.Log.Info("heads-up emitted", "date", date, "quality", quality)
	}
	return nil
}

// TestFire emits a test event (rate-limited upstream in admin).
func (m *AlertManager) TestFire(ctx context.Context) (bool, error) {
	id := ulid.New(time.Now())
	return m.Emit(&store.AlertEvent{
		EventKey: "test:" + id, LocalDate: time.Now().Format("2006-01-02"),
		Kind: "test", Title: "westward test", Body: "Delivery test from westward.",
	})
}

// reEncodeJPEG is the shared ladder step for attachment size caps.
func reEncodeJPEG(b []byte, maxW, quality int) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	bd := img.Bounds()
	if bd.Dx() <= maxW {
		var buf bytes.Buffer
		jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality})
		return buf.Bytes(), nil
	}
	h := bd.Dy() * maxW / bd.Dx()
	dst := image.NewRGBA(image.Rect(0, 0, maxW, h))
	draw.BiLinear.Scale(dst, dst.Bounds(), img, bd, draw.Src, nil)
	var buf bytes.Buffer
	jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality})
	return buf.Bytes(), nil
}

// NotifierDef is one runtime-editable notifier configuration (stored in the
// DB; secrets remain env var NAMES).
type NotifierDef struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // ntfy | pushover | webhook
	Enabled  bool   `json:"enabled"`
	Server   string `json:"server,omitempty"`
	Topic    string `json:"topic,omitempty"`
	TokenEnv string `json:"token_env,omitempty"`
	UserEnv  string `json:"user_env,omitempty"`
	URL      string `json:"url,omitempty"`
	HMACEnv  string `json:"hmac_env,omitempty"`
}

// LoadNotifierDefs returns DB defs, falling back to YAML defs on first boot.
func LoadNotifierDefs(st *store.Store, cfg config.Static) ([]NotifierDef, error) {
	var defs []NotifierDef
	ok, err := st.GetSettingRaw("notifiers", &defs)
	if err != nil {
		return nil, err
	}
	if ok && len(defs) > 0 {
		return defs, nil
	}
	for _, d := range cfg.Notifiers {
		defs = append(defs, NotifierDef(d))
	}
	return defs, nil
}

// BuildFromDefs instantiates notifiers from runtime defs.
func BuildFromDefs(defs []NotifierDef) (map[string]notify.Notifier, []string) {
	out := map[string]notify.Notifier{}
	var ids []string
	for _, d := range defs {
		if !d.Enabled {
			continue
		}
		switch d.Type {
		case "ntfy":
			out[d.ID] = &notify.Ntfy{Server: d.Server, Topic: d.Topic, TokenEnv: d.TokenEnv}
		case "pushover":
			out[d.ID] = &notify.Pushover{TokenEnv: d.TokenEnv, UserEnv: d.UserEnv, Encode: reEncodeJPEG}
		case "webhook":
			out[d.ID] = &notify.Webhook{URL: d.URL, HMACEnv: d.HMACEnv}
		}
		ids = append(ids, d.ID)
	}
	return out, ids
}

// Reload rebuilds the notifier set (called after admin edits).
func (m *AlertManager) Reload(defs []NotifierDef) {
	notifiers, ids := BuildFromDefs(defs)
	m.mu.Lock()
	m.Notifiers, m.IDs = notifiers, ids
	m.mu.Unlock()
}

// BuildNotifiers instantiates notifiers from config defs + runtime enablement.
func BuildNotifiers(cfg config.Static, settings config.Settings) (map[string]notify.Notifier, []string) {
	enabled := map[string]bool{}
	if settings.NotifierEnabled != nil {
		enabled = settings.NotifierEnabled
	}
	out := map[string]notify.Notifier{}
	var ids []string
	for _, d := range cfg.Notifiers {
		on := d.Enabled
		if settings.NotifierEnabled != nil {
			on = enabled[d.ID]
		}
		if !on {
			continue
		}
		switch d.Type {
		case "ntfy":
			out[d.ID] = &notify.Ntfy{Server: d.Server, Topic: d.Topic, TokenEnv: d.TokenEnv}
		case "pushover":
			out[d.ID] = &notify.Pushover{TokenEnv: d.TokenEnv, UserEnv: d.UserEnv, Encode: reEncodeJPEG}
		case "webhook":
			out[d.ID] = &notify.Webhook{URL: d.URL, HMACEnv: d.HMACEnv}
		}
		ids = append(ids, d.ID)
	}
	return out, ids
}
