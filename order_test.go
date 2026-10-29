package imagecore

import (
	"sort"
	"testing"
)

func TestCompareVersionsOrder(t *testing.T) {
	// Each version must sort before every version after it.
	ordered := []string{
		"1.2",
		"1.9",
		"1.9.1",
		"1.10",
		"1.10.0",
		"1.10.0-rc.1",
		"1.10.0-rc.2",
		"1.10.1",
		"1.35",
		"1.36",
		"1.100",
		"2",
		"2.0",
	}

	for i, a := range ordered {
		for j, b := range ordered {
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}

			if got := compareVersions(a, b); got != want {
				t.Errorf("compareVersions(%q, %q) = %d, want %d", a, b, got, want)
			}
		}
	}
}

func TestCompareVersionsNumbersHaveAnySize(t *testing.T) {
	small := "1.99999999999999999999999999"
	big := "1.100000000000000000000000000"

	if compareVersions(small, big) != -1 || compareVersions(big, small) != 1 {
		t.Error("long runs of digits must compare as numbers")
	}
}

func TestCompareVersionsLeadingZeros(t *testing.T) {
	if compareVersions("1.010", "1.9") != 1 {
		t.Error("1.010 is numerically above 1.9")
	}
	if compareVersions("1.010", "1.20") != -1 || compareVersions("1.20", "1.010") != 1 {
		t.Error("1.010 is numerically below 1.20, however many digits it has")
	}
	if compareVersions("1.0", "1.00") == 0 || compareVersions("1.000", "1.1") != -1 {
		t.Error("zeros must not count as digits of the number")
	}

	// Equal as numbers, so the text decides. The order must still be strict.
	a, b := "1.09", "1.9"
	if compareVersions(a, b) == 0 || compareVersions(a, b) != -compareVersions(b, a) {
		t.Errorf("%q and %q must be strictly ordered, and the same both ways", a, b)
	}
}

func TestCompareVersionsDigitsBeforeText(t *testing.T) {
	// Where a run of digits meets a run of other characters, digits come first.
	pairs := [][2]string{{"1", "a"}, {"1.2", "1.2a"}, {"1", "v1"}, {"9", "-1"}}

	for _, p := range pairs {
		if compareVersions(p[0], p[1]) != -1 || compareVersions(p[1], p[0]) != 1 {
			t.Errorf("%q must sort before %q", p[0], p[1])
		}
	}
}

func TestCompareVersionsEmpty(t *testing.T) {
	if compareVersions("", "") != 0 {
		t.Error("two empty versions are equal")
	}
	if compareVersions("", "1") != -1 || compareVersions("1", "") != 1 {
		t.Error("an empty version sorts first")
	}
}

// Sorting must give one result, whatever order the input is in. That needs a
// strict total order, with 0 only for identical strings.
func TestCompareVersionsIsATotalOrder(t *testing.T) {
	versions := []string{
		"", "0", "00", "1", "01", "1.0", "1.00", "1.09", "1.9", "1.10", "1.a",
		"1.a1", "1.a01", "1.0-rc", "1.0+x", "1.0_x", "v1.2", "V1.2", "10", "9", "a", "A", "1..2", "1.2",
	}

	for _, a := range versions {
		for _, b := range versions {
			ab, ba := compareVersions(a, b), compareVersions(b, a)

			if ab != -ba {
				t.Errorf("compareVersions(%q, %q) = %d but the reverse is %d", a, b, ab, ba)
			}
			if (ab == 0) != (a == b) {
				t.Errorf("compareVersions(%q, %q) = %d", a, b, ab)
			}

			for _, c := range versions {
				if ab < 0 && compareVersions(b, c) < 0 && compareVersions(a, c) >= 0 {
					t.Errorf("not transitive: %q < %q < %q, but %q is not below %q", a, b, c, a, c)
				}
			}
		}
	}

	// Sorting from several starting orders gives one answer.
	var first []string
	for shift := 0; shift < len(versions); shift++ {
		input := append(append([]string{}, versions[shift:]...), versions[:shift]...)
		sort.Slice(input, func(i, j int) bool { return compareVersions(input[i], input[j]) < 0 })

		if first == nil {
			first = input
			continue
		}
		for i := range input {
			if input[i] != first[i] {
				t.Fatalf("sort result depends on input order: %q vs %q", input, first)
			}
		}
	}
}

// An earlier difference decides, whatever follows it.
func TestCompareVersionsEarlierDifferenceDecides(t *testing.T) {
	pairs := [][2]string{
		{"1a2", "1b1"},
		{"1-rc.9", "1.0"}, // "-rc." is below "." as text, though 9 is above 0
		{"2.1", "10.0"},
	}

	for _, p := range pairs {
		if compareVersions(p[0], p[1]) != -1 || compareVersions(p[1], p[0]) != 1 {
			t.Errorf("%q must sort before %q", p[0], p[1])
		}
	}
}
