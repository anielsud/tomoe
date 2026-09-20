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
	jev       *JevAdjudicator // optional; nil disables adjudication
}

// SetJevAdjudicator installs an optional Jev-based topic adjudicator. The
// adjudicator is invoked only when the heuristic score alone is ambiguous
// (see Enrich). Passing nil disables it.
func (e *DefaultEnricher) SetJevAdjudicator(j *JevAdjudicator) {
	if e == nil {
		return
	}
	e.jev = j
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
//
// Flow:
//  1. List events from every provider in parallel, over an asymmetric window
//     around the session start.
//  2. Heuristic score (start alignment + URL + platform).
//  3. If a Jev adjudicator is installed AND scoring is ambiguous, ask Jev
//     for a topic match on each affected candidate. See scoresAreAmbiguous.
//  4. Return the top-scoring candidate above the threshold, or nil.
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

	// Jev topic adjudication — only when the heuristic alone is ambiguous.
	if e.jev != nil && in.Transcript != "" && scoresAreAmbiguous(scored, e.scoring.scoreThreshold) {
		e.applyJev(ctx, in.Transcript, scored)
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

// applyJev asks the adjudicator about every candidate that could plausibly
// become the winner. Errors are logged and the candidate keeps its
// heuristic-only score.
func (e *DefaultEnricher) applyJev(ctx context.Context, transcript string, scored []scoredEvent) {
	for i := range scored {
		bonus, err := e.jev.Score(ctx, transcript, scored[i].event)
		if err != nil {
			log.Printf("calendar: jev adjudication failed for %q: %v", scored[i].event.Title, err)
			continue
		}
		scored[i].total += bonus
		if scored[i].total > 100 {
			scored[i].total = 100
		}
	}
}

// scoresAreAmbiguous returns true when the heuristic result is worth
// spending a Jev call on: either the top score is below threshold but at
// least 20 (so a Jev boost could flip it), or the top two candidates are
// within 10 points of each other (a plausible tie).
func scoresAreAmbiguous(scored []scoredEvent, threshold int) bool {
	if len(scored) == 0 {
		return false
	}
	top := scored[0].total
	second := 0
	for _, s := range scored[1:] {
		if s.total > top {
			second = top
			top = s.total
		} else if s.total > second {
			second = s.total
		}
	}
	if top >= 20 && top < threshold {
		return true
	}
	if top >= threshold && (top-second) <= 10 && second >= 20 {
		return true
	}
	return false
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
