package tools

import (
	"fmt"
	"strings"
)

const diffContext = 2

func diff(before, after string) string {
	a := splitLines(before)
	b := splitLines(after)
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	type line struct {
		op   byte
		text string
		at   int
	}
	var ops []line
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			ops = append(ops, line{' ', a[i], i + 1})
			i++
			j++
		case j < m && (i == n || lcs[i][j+1] >= lcs[i+1][j]):
			ops = append(ops, line{'+', b[j], i + 1})
			j++
		default:
			ops = append(ops, line{'-', a[i], i + 1})
			i++
		}
	}
	keep := make([]bool, len(ops))
	for k, op := range ops {
		if op.op == ' ' {
			continue
		}
		for c := max(0, k-diffContext); c <= min(len(ops)-1, k+diffContext); c++ {
			keep[c] = true
		}
	}
	var out strings.Builder
	gap := true
	for k, op := range ops {
		if !keep[k] {
			gap = true
			continue
		}
		if gap {
			fmt.Fprintf(&out, "@@ line %d @@\n", op.at)
			gap = false
		}
		fmt.Fprintf(&out, "%c%s\n", op.op, op.text)
	}
	return out.String()
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
