package ocr

// glyphs is a tiny 5x7 bitmap font for the letters the model-backed test
// draws (no font dependency for one test).
var glyphs = map[rune][7]string{
	'A': {".###.", "#...#", "#...#", "#####", "#...#", "#...#", "#...#"},
	'E': {"#####", "#....", "#....", "####.", "#....", "#....", "#####"},
	'I': {"#####", "..#..", "..#..", "..#..", "..#..", "..#..", "#####"},
	'K': {"#...#", "#..#.", "#.#..", "##...", "#.#..", "#..#.", "#...#"},
	'L': {"#....", "#....", "#....", "#....", "#....", "#....", "#####"},
	'M': {"#...#", "##.##", "#.#.#", "#.#.#", "#...#", "#...#", "#...#"},
	'X': {"#...#", "#...#", ".#.#.", "..#..", ".#.#.", "#...#", "#...#"},
	' ': {".....", ".....", ".....", ".....", ".....", ".....", "....."},
}

// renderText draws s in white on a dark background, each font pixel scale
// image pixels, with a margin, as packed RGB.
func renderText(s string, scale int) ([]byte, int, int) {
	const margin = 3 // font pixels
	cols := len([]rune(s))*6 - 1 + 2*margin
	rows := 7 + 2*margin
	w, h := cols*scale, rows*scale
	pix := make([]byte, w*h*3)
	for i := 0; i < len(pix); i += 3 {
		pix[i], pix[i+1], pix[i+2] = 30, 30, 34
	}
	for n, r := range []rune(s) {
		g := glyphs[r]
		for gy := 0; gy < 7; gy++ {
			for gx := 0; gx < 5; gx++ {
				if g[gy][gx] != '#' {
					continue
				}
				for dy := 0; dy < scale; dy++ {
					for dx := 0; dx < scale; dx++ {
						x := (margin+n*6+gx)*scale + dx
						y := (margin+gy)*scale + dy
						i := (y*w + x) * 3
						pix[i], pix[i+1], pix[i+2] = 240, 240, 240
					}
				}
			}
		}
	}
	return pix, w, h
}
