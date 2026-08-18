package clock

import (
	"testing"
	"time"
)

func TestFakeAdvanceFiresInOrder(t *testing.T) {
	base := time.UnixMilli(1770000000000)
	f := NewFake(base)
	if !f.Now().Equal(base) {
		t.Fatal("now moved")
	}

	var order []int
	a := f.After(30 * time.Second)
	b := f.After(10 * time.Second)
	c := f.After(30 * time.Second) // same deadline as a, registered later

	// Advancing 10s fires only b.
	f.Advance(10 * time.Second)
	select {
	case <-b:
		order = append(order, 1)
	default:
		t.Fatal("b did not fire")
	}
	select {
	case <-a:
		t.Fatal("a fired early")
	default:
	}
	_ = c

	// Advancing 20s more fires a then c (same deadline, registration order).
	var fireTime time.Time
	f.Advance(20 * time.Second)
	select {
	case fireTime = <-a:
		order = append(order, 2)
	default:
		t.Fatal("a did not fire")
	}
	select {
	case <-c:
		order = append(order, 3)
	default:
		t.Fatal("c did not fire")
	}
	if len(order) != 3 || order[0] != 1 || order[1] != 2 || order[2] != 3 {
		t.Fatalf("order = %v", order)
	}

	// Channels carry their fire time.
	if fireTime.Sub(base) != 30*time.Second {
		t.Fatalf("fire time = %v", fireTime.Sub(base))
	}
}

func TestFakeNoEarlyFire(t *testing.T) {
	f := NewFake(time.UnixMilli(0))
	ch := f.After(time.Minute)
	f.Advance(59 * time.Second)
	select {
	case <-ch:
		t.Fatal("fired early")
	default:
	}
}
