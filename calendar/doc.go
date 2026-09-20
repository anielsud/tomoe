package calendar

// Example: implementing a custom Enricher.
//
// An external project that embeds Tomoe can plug in its own calendar
// resolver without depending on Tomoe internals. Register it after
// constructing the backend App and before Startup:
//
//	app := backend.NewApp()
//	app.SetCalendar(&staticEnricher{...})
//	// ... wails.Run(...) or app.Startup(ctx) ...
//
// The staticEnricher below returns a fixed set of events whose start times
// contain the session's start:
//
//	type staticEnricher struct{ events []*calendar.Event }
//
//	func (s *staticEnricher) Enrich(ctx context.Context, in calendar.MatchInput) (*calendar.Event, error) {
//	    for _, e := range s.events {
//	        if !in.StartTime.Before(e.StartTime) && !in.StartTime.After(e.EndTime) {
//	            return e, nil
//	        }
//	    }
//	    return nil, nil
//	}
