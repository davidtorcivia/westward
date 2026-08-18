package ulid

import (
	"regexp"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	now := time.UnixMilli(1770000000000)
	re := regexp.MustCompile(`^[0-9ABCDEFGHJKMNPQRSTVWXYZ]{26}$`)
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := New(now)
		if !re.MatchString(id) {
			t.Fatalf("bad format: %q", id)
		}
		if seen[id] {
			t.Fatalf("collision within same ms: %q", id)
		}
		seen[id] = true
	}
	// same instant ULIDs sort monotonically
	a, b := New(now), New(now)
	if a >= b {
		t.Fatalf("not monotonic within ms: %q >= %q", a, b)
	}
	c := New(now.Add(time.Millisecond))
	if c <= b {
		t.Fatalf("not monotonic across ms: %q <= %q", c, b)
	}
}
