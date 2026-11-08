package units

import (
	"fmt"
	"strings"
)

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

type op struct {
	kind byte // ' ', '-', '+'
	text string
}

// UnifiedDiff returns a unified diff (3 lines of context) or "" if equal.
func UnifiedDiff(nameA, nameB, a, b string) string {
	if a == b {
		return ""
	}
	A, B := splitLines(a), splitLines(b)
	n, m := len(A), len(B)
	var ops []op
	if n*m > 25_000_000 { // too big for the DP table: replace whole file
		for _, l := range A {
			ops = append(ops, op{'-', l})
		}
		for _, l := range B {
			ops = append(ops, op{'+', l})
		}
	} else {
		lcs := make([][]int32, n+1)
		for i := range lcs {
			lcs[i] = make([]int32, m+1)
		}
		for i := n - 1; i >= 0; i-- {
			for j := m - 1; j >= 0; j-- {
				if A[i] == B[j] {
					lcs[i][j] = lcs[i+1][j+1] + 1
				} else if lcs[i+1][j] >= lcs[i][j+1] {
					lcs[i][j] = lcs[i+1][j]
				} else {
					lcs[i][j] = lcs[i][j+1]
				}
			}
		}
		i, j := 0, 0
		for i < n && j < m {
			switch {
			case A[i] == B[j]:
				ops = append(ops, op{' ', A[i]})
				i++
				j++
			case lcs[i+1][j] >= lcs[i][j+1]:
				ops = append(ops, op{'-', A[i]})
				i++
			default:
				ops = append(ops, op{'+', B[j]})
				j++
			}
		}
		for ; i < n; i++ {
			ops = append(ops, op{'-', A[i]})
		}
		for ; j < m; j++ {
			ops = append(ops, op{'+', B[j]})
		}
	}
	const ctx = 3
	var changes []int
	for i, o := range ops {
		if o.kind != ' ' {
			changes = append(changes, i)
		}
	}
	if len(changes) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", nameA, nameB)
	for g := 0; g < len(changes); {
		first := changes[g]
		last := first
		g++
		for g < len(changes) && changes[g]-last <= 2*ctx {
			last = changes[g]
			g++
		}
		s, e := max(first-ctx, 0), min(last+ctx+1, len(ops))
		aStart, bStart := 0, 0
		for _, o := range ops[:s] {
			if o.kind != '+' {
				aStart++
			}
			if o.kind != '-' {
				bStart++
			}
		}
		aLen, bLen := 0, 0
		for _, o := range ops[s:e] {
			if o.kind != '+' {
				aLen++
			}
			if o.kind != '-' {
				bLen++
			}
		}
		as, bs := aStart+1, bStart+1
		if aLen == 0 {
			as = aStart
		}
		if bLen == 0 {
			bs = bStart
		}
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", as, aLen, bs, bLen)
		for _, o := range ops[s:e] {
			sb.WriteByte(o.kind)
			sb.WriteString(o.text)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}
