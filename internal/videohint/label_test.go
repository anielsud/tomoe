package videohint

import (
	"reflect"
	"testing"
)

func TestCleanOCRNameDropsSymbolScraps(t *testing.T) {
	for in, want := range map[string]string{"Alex Kim •.•": "Alex Kim", "Alex Kim *•.": "Alex Kim", "Priya Desai fo": "Priya Desai", "Ana Lopez": "Ana Lopez"} {
		if got := cleanOCRName(in); got != want {
			t.Errorf("cleanOCRName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLabelRect(t *testing.T) {
	ring := RingMatch{X: 1386, Y: 97, Width: 130, Height: 130}
	label := LabelRegion{BottomOffset: 52, Height: 40, MaxWidth: 300}

	x, y, w, h := LabelRect(ring, label)
	// MaxWidth (300) is wider than the ring (130), so the crop clamps
	// to the ring's own width rather than spilling past it.
	if x != ring.X || w != ring.Width {
		t.Errorf("LabelRect() x,w = %d,%d, want %d,%d (clamped to ring width)", x, w, ring.X, ring.Width)
	}
	if y != ring.Y+ring.Height-52 {
		t.Errorf("LabelRect() y = %d, want %d", y, ring.Y+ring.Height-52)
	}
	if h != 40 {
		t.Errorf("LabelRect() h = %d, want 40", h)
	}
}

func TestLabelRect_NarrowerThanRing(t *testing.T) {
	ring := RingMatch{X: 5, Y: 97, Width: 1794, Height: 1026}
	label := LabelRegion{BottomOffset: 52, Height: 40, MaxWidth: 300}

	x, y, w, h := LabelRect(ring, label)
	if x != ring.X || w != 300 {
		t.Errorf("LabelRect() x,w = %d,%d, want %d,%d (MaxWidth, not clamped)", x, w, ring.X, 300)
	}
	if y != ring.Y+ring.Height-52 || h != 40 {
		t.Errorf("LabelRect() y,h = %d,%d, want %d,%d", y, h, ring.Y+ring.Height-52, 40)
	}
}

func TestCropRGB(t *testing.T) {
	const width, height = 10, 10
	pix := make([]byte, width*height*3)
	for i := 0; i < width*height; i++ {
		pix[i*3] = byte(i) // encode pixel index into red channel
	}

	crop, cw, ch, err := cropRGB(pix, width, height, 2, 3, 4, 2)
	if err != nil {
		t.Fatalf("cropRGB() error = %v", err)
	}
	if cw != 4 || ch != 2 {
		t.Fatalf("cropRGB() size = %dx%d, want 4x2", cw, ch)
	}
	// First cropped pixel should be the source pixel at (2,3): index 3*10+2=32.
	if crop[0] != 32 {
		t.Errorf("crop[0] (red channel) = %d, want 32", crop[0])
	}
	// Second row's first pixel: source (2,4): index 4*10+2=42.
	if crop[1*4*3] != 42 {
		t.Errorf("crop row1 first pixel red channel = %d, want 42", crop[1*4*3])
	}
}

func TestCropRGB_ClampsToFrameBounds(t *testing.T) {
	const width, height = 10, 10
	pix := make([]byte, width*height*3)

	// Rectangle runs past the right and bottom edges.
	crop, cw, ch, err := cropRGB(pix, width, height, 8, 8, 5, 5)
	if err != nil {
		t.Fatalf("cropRGB() error = %v", err)
	}
	if cw != 2 || ch != 2 {
		t.Errorf("cropRGB() clamped size = %dx%d, want 2x2", cw, ch)
	}
	if len(crop) != cw*ch*3 {
		t.Errorf("len(crop) = %d, want %d", len(crop), cw*ch*3)
	}
}

func TestCropRGB_EmptyAfterClamping(t *testing.T) {
	const width, height = 10, 10
	pix := make([]byte, width*height*3)

	if _, _, _, err := cropRGB(pix, width, height, 20, 20, 5, 5); err == nil {
		t.Error("cropRGB() with an entirely out-of-bounds rectangle: want error, got nil")
	}
}

func TestCleanOCRName(t *testing.T) {
	cases := []struct {
		raw, want string
	}{
		{"Devin Dobrowolski Priv", "Devin Dobrowolski"},
		{"Devin Dobrowolski Privacy", "Devin Dobrowolski"},
		{"Natalia Ramirez Muted", "Natalia Ramirez"},
		{"Natalia Ramirez Recording", "Natalia Ramirez"},
		{"Daniel Stone", "Daniel Stone"}, // no noise word, unchanged
		{"Privacy", "Privacy"},           // single word alone is never stripped
		{"  Devin Dobrowolski Priv  ", "Devin Dobrowolski"},
	}
	for _, c := range cases {
		if got := cleanOCRName(c.raw); got != c.want {
			t.Errorf("cleanOCRName(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestPlausibleName(t *testing.T) {
	for name, want := range map[string]bool{
		"Alex Kim":                      true,
		"Ana":                           true,
		"Jo Share":                      true, // one toolbar word is a surname, not the toolbar
		"ake control Annotate":          false,
		"Take control Annotate Pop out": false,
		"Take control":                  false,
		"E":                             false,
		"-":                             false,
		"":                              false,
	} {
		if got := plausibleName(name); got != want {
			t.Errorf("plausibleName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestRecognizeLabel_RingTooShort(t *testing.T) {
	// A popup covering the lit tile leaves a ring a few pixels tall; its
	// label box would sit on the toolbar above.
	label := LabelRegion{BottomOffset: 52, Height: 40, MaxWidth: 300}
	pix := make([]byte, 400*300*3)
	if _, err := RecognizeLabel(pix, 400, 300, RingMatch{X: 10, Y: 100, Width: 129, Height: 6}, label); err != errRingTooShort {
		t.Errorf("short ring: err %v, want errRingTooShort", err)
	}
}

func TestLookAccepted(t *testing.T) {
	short := RingMatch{X: 728, Y: 97, Width: 129, Height: 6}
	tile := RingMatch{X: 327, Y: 97, Width: 129, Height: 130}
	cases := []struct {
		look Look
		name string
		cand []string
	}{
		{Look{Usable: true, Name: "Alex Kim", Ring: &tile}, "Alex Kim", nil},
		{Look{Usable: true, Name: "Alex Kim", Ring: &short}, "", nil},                          // read from the toolbar's position
		{Look{Usable: true, Name: "Alex Kim", Ring: &short, FromCache: true}, "Alex Kim", nil}, // read earlier from the whole tile
		{Look{Usable: true, Name: "ake control Annotate", Ring: &tile}, "", nil},
		{Look{Rings: []RingMatch{tile, short}, Candidates: []string{"Priya Desa...", "Alex Kim"}}, "", []string{"Priya Desa...", ""}},
	}
	for i, c := range cases {
		name, cand := c.look.Accepted()
		if name != c.name || !reflect.DeepEqual(cand, c.cand) {
			t.Errorf("case %d: Accepted() = %q, %q; want %q, %q", i, name, cand, c.name, c.cand)
		}
	}
}
