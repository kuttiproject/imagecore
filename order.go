package imagecore

import "strings"

// compareVersions orders two versions the way a person would, and returns -1,
// 0 or 1. Each version is cut into runs of digits and runs of anything else.
// Runs of digits are compared as numbers, so "1.9" comes before "1.10", and
// other runs as text. If one version is a prefix of the other, the shorter one
// comes first. This is not semantic versioning, which the image lists do not
// need: only released Kubernetes versions are published.
//
// The result is 0 only for identical strings, so sorting with it gives the
// same order whatever the order of the input.
func compareVersions(a, b string) int {
	x, y := a, b

	for x != "" && y != "" {
		tx, rx := splitRun(x)
		ty, ry := splitRun(y)

		switch dx, dy := isDigit(tx[0]), isDigit(ty[0]); {
		case dx && dy:
			if c := compareNumbers(tx, ty); c != 0 {
				return c
			}
		case dx:
			return -1
		case dy:
			return 1
		default:
			if c := strings.Compare(tx, ty); c != 0 {
				return c
			}
		}

		x, y = rx, ry
	}

	switch {
	case x == "" && y != "":
		return -1
	case x != "" && y == "":
		return 1
	}

	return strings.Compare(a, b)
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

// splitRun returns the leading run of digits, or of non-digits, in a non-empty
// string, and the rest of the string.
func splitRun(s string) (string, string) {
	digit := isDigit(s[0])

	i := 1
	for i < len(s) && isDigit(s[i]) == digit {
		i++
	}

	return s[:i], s[i:]
}

// compareNumbers compares two runs of digits as numbers of any size.
func compareNumbers(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")

	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}

	return strings.Compare(a, b)
}
