package eval

import "math"

// maxWeightMatching assigns rows to distinct columns to maximize the total
// weight (the Hungarian algorithm), returning each row's column or -1 when
// there are more rows than columns. Used to match hypothesis speakers to
// reference speakers the way the standard diarization error rate does.
func maxWeightMatching(w [][]float64) []int {
	rows := len(w)
	if rows == 0 {
		return nil
	}
	cols := len(w[0])
	n := max(rows, cols)
	maxW := 0.0
	for _, r := range w {
		for _, v := range r {
			maxW = math.Max(maxW, v)
		}
	}
	// Square min-cost problem: cost = maxW - weight, padding with maxW
	// (weight 0) for missing rows/columns.
	cost := make([][]float64, n+1)
	for i := range cost {
		cost[i] = make([]float64, n+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= n; j++ {
			v := 0.0
			if i <= rows && j <= cols {
				v = w[i-1][j-1]
			}
			cost[i][j] = maxW - v
		}
	}

	// Classic O(n^3) potentials formulation, 1-indexed.
	u := make([]float64, n+1)
	v := make([]float64, n+1)
	p := make([]int, n+1) // p[j]: row matched to column j
	way := make([]int, n+1)
	for i := 1; i <= n; i++ {
		p[0] = i
		j0 := 0
		minv := make([]float64, n+1)
		used := make([]bool, n+1)
		for j := range minv {
			minv[j] = math.Inf(1)
		}
		for {
			used[j0] = true
			i0, delta, j1 := p[j0], math.Inf(1), 0
			for j := 1; j <= n; j++ {
				if used[j] {
					continue
				}
				cur := cost[i0][j] - u[i0] - v[j]
				if cur < minv[j] {
					minv[j], way[j] = cur, j0
				}
				if minv[j] < delta {
					delta, j1 = minv[j], j
				}
			}
			for j := 0; j <= n; j++ {
				if used[j] {
					u[p[j]] += delta
					v[j] -= delta
				} else {
					minv[j] -= delta
				}
			}
			j0 = j1
			if p[j0] == 0 {
				break
			}
		}
		for j0 != 0 {
			j1 := way[j0]
			p[j0] = p[j1]
			j0 = j1
		}
	}

	assign := make([]int, rows)
	for i := range assign {
		assign[i] = -1
	}
	for j := 1; j <= n; j++ {
		if i := p[j]; i >= 1 && i <= rows && j <= cols {
			assign[i-1] = j - 1
		}
	}
	return assign
}
