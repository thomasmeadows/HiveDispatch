package supervisor

import "strings"

// Diff is a line-based diff for showing an operator what write_config is
// about to change. It is for humans; its exact shape is not a contract.
func Diff(name, a, b string) string {
	al, bl := splitLines(a), splitLines(b)
	// Longest common subsequence table.
	n, m := len(al), len(bl)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if al[i] == bl[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var sb strings.Builder
	if a == "" {
		sb.WriteString("--- " + name + " (missing)\n")
	} else {
		sb.WriteString("--- " + name + "\n")
	}
	sb.WriteString("+++ " + name + "\n")
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case al[i] == bl[j]:
			sb.WriteString(" " + al[i] + "\n")
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			sb.WriteString("-" + al[i] + "\n")
			i++
		default:
			sb.WriteString("+" + bl[j] + "\n")
			j++
		}
	}
	for ; i < n; i++ {
		sb.WriteString("-" + al[i] + "\n")
	}
	for ; j < m; j++ {
		sb.WriteString("+" + bl[j] + "\n")
	}
	return sb.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
