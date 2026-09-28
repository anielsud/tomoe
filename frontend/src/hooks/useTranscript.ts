import { useState, useEffect, useCallback } from 'react';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import { Segment } from '../types';

// How settled a segment's text is: "live" < "pending" < final.
function statusRank(status?: string): number {
  if (status === 'live') return 0;
  if (status === 'pending') return 1;
  return 2;
}

// New segments and revisions of them (same id) arrive as separate events
// that can be delivered in either order, so both are applied as an
// upsert: replace a known id unless that would regress it to a less
// settled status, otherwise insert in start-time order. Mirrors
// session.Session.UpsertSegment on the Go side.
export function upsertSegment(prev: Segment[], seg: Segment): Segment[] {
  const i = prev.findIndex(s => s.id === seg.id);
  if (i >= 0) {
    if (statusRank(prev[i].status) > statusRank(seg.status)) return prev;
    const next = prev.slice();
    next[i] = seg;
    return next;
  }
  let at = prev.length;
  while (at > 0 && prev[at - 1].start_time > seg.start_time) at--;
  return [...prev.slice(0, at), seg, ...prev.slice(at)];
}

export function useTranscript() {
  const [segments, setSegments] = useState<Segment[]>([]);

  useEffect(() => {
    const cancelNew = EventsOn('transcript:segment', (seg: Segment) => {
      setSegments(prev => upsertSegment(prev, seg));
    });
    // Two-pass transcription: a later, higher-fidelity re-decode of a
    // segment already shown (same id) supersedes it in place, rather
    // than appending a duplicate line — see internal/live's Status field.
    const cancelUpdate = EventsOn('transcript:segment:update', (seg: Segment) => {
      setSegments(prev => upsertSegment(prev, seg));
    });

    return () => {
      cancelNew();
      cancelUpdate();
    };
  }, []);

  const clear = useCallback(() => {
    setSegments([]);
  }, []);

  return { segments, clear };
}
