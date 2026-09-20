package calendar

import (
	"testing"
	"time"

	"github.com/sosuke-ai/tomoe-pc/calendar"
)

func TestProviderCacheHitAndMiss(t *testing.T) {
	c := newProviderCache(time.Second)
	from := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	to := from.Add(2 * time.Hour)
	events := []calendar.Event{{Title: "A"}, {Title: "B"}}

	if _, ok := c.get("p", from, to); ok {
		t.Fatal("miss expected on fresh cache")
	}
	c.set("p", from, to, events)

	got, ok := c.get("p", from, to)
	if !ok {
		t.Fatal("expected hit")
	}
	if len(got) != 2 || got[0].Title != "A" || got[1].Title != "B" {
		t.Errorf("cached events differ: %+v", got)
	}

	// Different key = miss.
	if _, ok := c.get("q", from, to); ok {
		t.Error("wrong provider key hit")
	}
	if _, ok := c.get("p", from.Add(time.Minute), to); ok {
		t.Error("wrong from key hit")
	}
	if _, ok := c.get("p", from, to.Add(time.Minute)); ok {
		t.Error("wrong to key hit")
	}
}

func TestProviderCacheExpiration(t *testing.T) {
	c := newProviderCache(30 * time.Millisecond)
	from := time.Now()
	to := from.Add(time.Hour)
	c.set("p", from, to, []calendar.Event{{Title: "X"}})

	if _, ok := c.get("p", from, to); !ok {
		t.Fatal("hit expected immediately after set")
	}
	time.Sleep(50 * time.Millisecond)
	if _, ok := c.get("p", from, to); ok {
		t.Error("expected miss after TTL")
	}
}

func TestProviderCacheReturnsCopy(t *testing.T) {
	c := newProviderCache(time.Minute)
	from := time.Now()
	to := from.Add(time.Hour)
	c.set("p", from, to, []calendar.Event{{Title: "A"}})

	got, _ := c.get("p", from, to)
	got[0].Title = "MUTATED"

	again, _ := c.get("p", from, to)
	if again[0].Title != "A" {
		t.Errorf("cache was mutated by caller: %q", again[0].Title)
	}
}

func TestProviderCacheZeroTTLDisables(t *testing.T) {
	c := newProviderCache(0)
	c.set("p", time.Now(), time.Now(), []calendar.Event{{Title: "X"}})
	if _, ok := c.get("p", time.Now(), time.Now()); ok {
		t.Error("zero TTL should disable caching")
	}
}

func TestProviderCacheNilSafe(t *testing.T) {
	var c *providerCache
	if _, ok := c.get("p", time.Now(), time.Now()); ok {
		t.Error("nil cache should always miss")
	}
	c.set("p", time.Now(), time.Now(), nil) // should not panic
}
