// Package solar computes sunset and civil dusk. The engine's own computation
// is the canonical schedule; provider event times are stored for comparison only.
package solar

import (
	"time"

	astral "github.com/sj14/astral/pkg/astral"
)

// Events for one local date.
type Events struct {
	Date   time.Time // midnight local of the date
	Sunset time.Time
	Dusk   time.Time // civil dusk, sun 6° below horizon
}

// For returns sunset and civil dusk for the local calendar day containing t.
// Near-polar latitudes can produce errors from the underlying model; the
// caller (a temperate fixed site) treats an error as unschedulable.
func For(loc *time.Location, lat, lon float64, day time.Time) (Events, error) {
	d := time.Date(day.In(loc).Year(), day.In(loc).Month(), day.In(loc).Day(), 0, 0, 0, 0, loc)
	obs := astral.Observer{Latitude: lat, Longitude: lon, Elevation: 0}
	ss, err := astral.Sunset(obs, d)
	if err != nil {
		return Events{}, err
	}
	dusk, err := astral.Dusk(obs, d, 6)
	if err != nil {
		return Events{}, err
	}
	return Events{Date: d, Sunset: ss, Dusk: dusk}, nil
}
