// Package axtree reads another app's accessibility tree: the roles,
// descriptions and on-screen boxes of its elements, as screen readers see
// them. Meeting apps describe their participant tiles there (name, mic and
// camera state) in plain text, so nothing has to be read off pixels.
package axtree

// Rect is an element's box in global screen points (origin top left of
// the main display; other displays can be negative or beyond its width).
type Rect struct {
	X, Y, Width, Height float64
}

// Element is one node of the tree that has a description or is a window.
type Element struct {
	Role        string `json:"role"`
	Description string `json:"description,omitempty"`
	Title       string `json:"title,omitempty"`
	Rect        Rect   `json:"rect"`
}
