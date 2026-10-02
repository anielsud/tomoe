package videohint

// isBlank reports whether a captured frame is black: windows that ask not
// to be captured (the window server's sharing state 0), and some floating
// panels, come back as all-black pixels of the right size. Fewer than
// 0.2% of sampled pixels brighter than a JPEG-noise floor count as black.
func isBlank(pix []byte, width, height int) bool {
	if width <= 0 || height <= 0 || len(pix) < width*height*3 {
		return false
	}
	const step = 7
	var n, lit int
	for y := 0; y < height; y += step {
		for x := 0; x < width; x += step {
			i := (y*width + x) * 3
			n++
			if 299*int(pix[i])+587*int(pix[i+1])+114*int(pix[i+2]) > 12*1000 {
				lit++
			}
		}
	}
	return n > 0 && lit*500 < n
}
