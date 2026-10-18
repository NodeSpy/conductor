package bot

import (
	"fmt"
	"strings"
)

// UnifiedDiff is a line diff of a → b in unified form (3 lines of context),
// for an operator reading a proposal. Memory documents are small (capped),
// so the plain LCS table is fine.
func UnifiedDiff(a, b string) string {
	al, bl := splitLines(a), splitLines(b)
	n, m := len(al), len(bl)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if al[i] == bl[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	type op struct {
		k    byte // ' ', '-', '+'
		text string
		ai   int
		bi   int
	}
	var ops []op
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && al[i] == bl[j]:
			ops = append(ops, op{' ', al[i], i, j})
			i++
			j++
		case j < m && (i == n || lcs[i][j+1] > lcs[i+1][j]):
			ops = append(ops, op{'+', bl[j], i, j})
			j++
		default:
			ops = append(ops, op{'-', al[i], i, j})
			i++
		}
	}
	const ctx = 3
	var out strings.Builder
	for k := 0; k < len(ops); {
		if ops[k].k == ' ' {
			k++
			continue
		}
		start := k - ctx
		if start < 0 {
			start = 0
		}
		end := k
		for end < len(ops) {
			if ops[end].k != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].k == ' ' {
				run++
			}
			if run == len(ops) || run-end > 2*ctx {
				end += ctx
				if end > len(ops) {
					end = len(ops)
				}
				break
			}
			end = run
		}
		as, bs, ac, bc := ops[start].ai+1, ops[start].bi+1, 0, 0
		for _, o := range ops[start:end] {
			if o.k != '+' {
				ac++
			}
			if o.k != '-' {
				bc++
			}
		}
		if ac == 0 {
			as--
		}
		if bc == 0 {
			bs--
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", as, ac, bs, bc)
		for _, o := range ops[start:end] {
			out.WriteByte(o.k)
			out.WriteString(o.text)
			out.WriteByte('\n')
		}
		k = end
	}
	return out.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
