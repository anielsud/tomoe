package live

import "testing"

func TestChangeLog(t *testing.T) {
	var l ChangeLog
	l.Add(20)
	l.Add(5)
	l.NameSeen(10, "Ana")
	l.NameSeen(11, "Ana") // same name: no change
	l.NameSeen(30, "Ben") // a change at 30
	for _, c := range []struct {
		from, to float64
		want     bool
	}{{0, 4, false}, {4, 6, true}, {6, 19, false}, {19, 21, true}, {25, 31, true}, {31, 40, false}} {
		if got := l.Between(c.from, c.to); got != c.want {
			t.Errorf("Between(%v, %v) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}

func TestChangeLogTruncatedNames(t *testing.T) {
	var l ChangeLog
	l.NameSeen(1, "Jennifer Hem...")
	l.NameSeen(2, "Jennifer Hem..")
	l.NameSeen(3, "jennifer hem…")
	l.NameSeen(4, "Julianne DeVin..")
	if got := l.NameChangesIn(0, 10); len(got) != 1 || got[0] != 4 {
		t.Errorf("changes %v, want just the one at 4", got)
	}
}
