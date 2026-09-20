package calendar

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/sosuke-ai/tomoe-pc/calendar"
	"github.com/sosuke-ai/tomoe-pc/internal/config"
)

// DefaultEnricher is Tomoe's built-in calendar.Enricher. It composes one or
// more Providers, lists events in an asymmetric window around the session
// start, scores each, and returns the top match above the configured
// threshold (or nil).
//
// Safe for concurrent Enrich calls; each call may fan out to Providers in
// parallel.
type DefaultEnricher struct {
	providers []Provider
	scoring   scoringConfig
	cache     *providerCache
}

// NewDefaultEnricher constructs a DefaultEnricher from a CalendarConfig plus
// concrete Providers. Callers wire providers based on cfg.Providers (e.g.
// "ical" → ical.New(...) — done by the backend, not here, to keep this
// package's dependencies bounded).
func NewDefaultEnricher(cfg config.CalendarConfig, providers []Provider) (*DefaultEnricher, error) {
	if len(providers) == 0 {
		return nil, errors.New("calendar: no providers configured")
	}
	startWindow := time.Duration(cfg.MatchStartWindowMinutes) * time.Minute
	if startWindow <= 0 {
		startWindow = 15 * time.Minute
	}
	ttl := time.Duration(cfg.CacheTTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	threshold := cfg.MatchScoreThreshold
	if threshold <= 0 {
		threshold = 40
	}
	return &DefaultEnricher{
		providers: providers,
		scoring: scoringConfig{
			startWindow:    startWindow,
			endBounded:     cfg.MatchEndWindowBound,
			scoreThreshold: threshold,
		},
		cache: newProviderCache(ttl),
	}, nil
}

// Enrich implements calendar.Enricher.
func (e *DefaultEnricher) Enrich(ctx context.Context, in calendar.MatchInput) (*calendar.Event, error) {
	if e == nil || len(e.providers) == 0 {
		return nil, nil
	}
	if in.StartTime.IsZero() {
		return nil, nil
	}

	from, to := candidateWindow(in.StartTime, e.scoring.startWindow, e.scoring.endBounded)

	scored := e.listAndScore(ctx, in, from, to)
	if len(scored) == 0 {
		return nil, nil
	}
	best := pickBest(scored, e.scoring.scoreThreshold)
	if best == nil {
		return nil, nil
	}
	out := best.event
	out.Provider = best.provider
	out.MatchedAt = time.Now()
	out.MatchScore = best.total
	return &out, nil
}

// listAndScore fans out to every Provider in parallel, then scores. Provider
// errors are logged and skipped so a broken feed doesn't fail the whole
// enrichment.
func (e *DefaultEnricher) listAndScore(ctx context.Context, in calendar.MatchInput, from, to time.Time) []scoredEvent {
	type providerResult struct {
		provider string
		events   []calendar.Event
	}
	results := make([]providerResult, len(e.providers))

	var wg sync.WaitGroup
	for i, p := range e.providers {
		wg.Add(1)
		go func(i int, p Provider) {
			defer wg.Done()
			events, err := e.listWithCache(ctx, p, from, to)
			if err != nil {
				log.Printf("calendar: provider %q failed: %v", p.Name(), err)
				return
			}
			results[i] = providerResult{provider: p.Name(), events: events}
		}(i, p)
	}
	wg.Wait()

	var scored []scoredEvent
	for _, r := range results {
		for _, ev := range r.events {
			total := score(ev, in, e.scoring)
			if total <= 0 {
				continue
			}
			scored = append(scored, scoredEvent{event: ev, total: total, provider: r.provider})
		}
	}
	return scored
}

func (e *DefaultEnricher) listWithCache(ctx context.Context, p Provider, from, to time.Time) ([]calendar.Event, error) {
	if cached, ok := e.cache.get(p.Name(), from, to); ok {
		return cached, nil
	}
	events, err := p.ListEvents(ctx, from, to)
	if err != nil {
		return nil, err
	}
	e.cache.set(p.Name(), from, to, events)
	return events, nil
}

// Ensure DefaultEnricher satisfies the public interface at compile time.
var _ calendar.Enricher = (*DefaultEnricher)(nil)

// ProviderList is a convenience returning provider names — useful when
// logging the current configuration.
func (e *DefaultEnricher) ProviderList() []string {
	if e == nil {
		return nil
	}
	names := make([]string, len(e.providers))
	for i, p := range e.providers {
		names[i] = p.Name()
	}
	return names
}
