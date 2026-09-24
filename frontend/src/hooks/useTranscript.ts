import { useState, useEffect, useCallback } from 'react';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import { Segment } from '../types';

export function useTranscript() {
  const [segments, setSegments] = useState<Segment[]>([]);

  useEffect(() => {
    const cancelNew = EventsOn('transcript:segment', (seg: Segment) => {
      setSegments(prev => [...prev, seg]);
    });
    // Two-pass transcription: a later, higher-fidelity re-decode of a
    // segment already shown (same id) supersedes it in place, rather
    // than appending a duplicate line — see internal/live's Status field.
    const cancelUpdate = EventsOn('transcript:segment:update', (seg: Segment) => {
      setSegments(prev => prev.map(s => (s.id === seg.id ? seg : s)));
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
