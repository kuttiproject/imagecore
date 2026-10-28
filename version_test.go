package imagecore

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidVersionAccepts(t *testing.T) {
	cases := []string{
		"1.28.4",
		"1.30.0-rc.1",
		"v1.29.2",
		"1.31.0+build.5",
		"1.2.3_4",
		"0",
		"a",
		"Z9",
		"1..2", // odd, but harmless as part of a file name
		strings.Repeat("1", maxVersionLength),
	}

	for _, v := range cases {
		if !validVersion(v) {
			t.Errorf("validVersion(%q) = false, want true", v)
		}
	}
}

func TestValidVersionRejects(t *testing.T) {
	cases := map[string]string{
		"empty":                "",
		"too long":             strings.Repeat("1", maxVersionLength+1),
		"leading dot":          ".1",
		"dot":                  ".",
		"dotdot":               "..",
		"leading dash":         "-1",
		"leading underscore":   "_1",
		"leading plus":         "+1",
		"slash":                "1/2",
		"leading slash":        "/1",
		"traversal":            "../1",
		"backslash":            `1\2`,
		"drive prefix":         "c:1",
		"colon":                "1:2",
		"space":                "1 2",
		"trailing newline":     "1.2\n",
		"tab":                  "1\t2",
		"NUL":                  "1\x002",
		"percent":              "1%2e",
		"star":                 "1*",
		"non-ASCII letter":     "1.2é",
		"non-ASCII digit":      "١.٢",
		"leading non-ASCII":    "é1",
		"quote":                `1"2`,
		"long and has a slash": strings.Repeat("1", 70) + "/",
	}

	for name, v := range cases {
		if validVersion(v) {
			t.Errorf("%s: validVersion(%q) = true, want false", name, v)
		}
	}
}

// The limit is on the length in bytes, so that a name made from a version is
// bounded whatever the characters are. All accepted characters are one byte.
func TestValidVersionLengthBoundary(t *testing.T) {
	if !validVersion(strings.Repeat("a", 64)) {
		t.Error("64 characters should be accepted")
	}
	if validVersion(strings.Repeat("a", 65)) {
		t.Error("65 characters should be rejected")
	}
}

func listOf(versions ...string) map[string]*Image {
	list := map[string]*Image{}
	for _, v := range versions {
		list[v] = &Image{k8sVersion: v}
	}
	return list
}

func TestCheckImageListAccepts(t *testing.T) {
	oldList := map[string]*Image{}
	if err := json.Unmarshal([]byte(oldFormat), &oldList); err != nil {
		t.Fatal(err)
	}

	cases := map[string]map[string]*Image{
		"nil":        nil,
		"empty":      {},
		"one":        listOf("1.30.1"),
		"several":    listOf("1.28.4", "1.29.2", "1.30.1", "1.31.0-rc.1"),
		"old format": oldList,
	}

	for name, l := range cases {
		if err := checkImageList("test list", l); err != nil {
			t.Errorf("%s: unexpected error: %v", name, err)
		}
	}
}

func TestCheckImageListRejects(t *testing.T) {
	cases := []struct {
		name string
		list map[string]*Image
		want string // the entry the error must name
	}{
		{"empty key", map[string]*Image{"": {k8sVersion: ""}}, `""`},
		{"key with slash", map[string]*Image{"1/2": {k8sVersion: "1/2"}}, `"1/2"`},
		{"key with traversal", map[string]*Image{"../x": {k8sVersion: "../x"}}, `"../x"`},
		{"key too long", map[string]*Image{strings.Repeat("1", 65): {k8sVersion: strings.Repeat("1", 65)}}, strings.Repeat("1", 65)},
		{"nil entry", map[string]*Image{"1.30.1": nil}, `"1.30.1"`},
		{"entry for another version", map[string]*Image{"1.30.1": {k8sVersion: "1.30.2"}}, `"1.30.1"`},
		{"entry without version", map[string]*Image{"1.30.1": {}}, `"1.30.1"`},
		{"entry version differs in case", map[string]*Image{"v1.30.1": {k8sVersion: "V1.30.1"}}, `"v1.30.1"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkImageList("test list", tc.list)
			if err == nil {
				t.Fatal("expected an error")
			}
			msg := err.Error()
			if !strings.Contains(msg, "test list") {
				t.Errorf("error %q does not name the source", msg)
			}
			if !strings.Contains(msg, tc.want) {
				t.Errorf("error %q does not name the entry %s", msg, tc.want)
			}
		})
	}
}

// One invalid entry among valid ones rejects the list, wherever it sorts.
func TestCheckImageListOneBadEntryRejectsAll(t *testing.T) {
	for _, bad := range []string{"0.0.1", "1.29.2", "9.9.9"} {
		list := listOf("1.28.4", "1.29.2", "1.30.1")
		list[bad] = &Image{k8sVersion: "something-else"}

		if err := checkImageList("test list", list); err == nil {
			t.Errorf("a bad entry %q should reject the list", bad)
		}
	}
}

// The entry named in the error must not depend on map iteration order.
func TestCheckImageListNamesFirstBadEntryInKeyOrder(t *testing.T) {
	list := listOf("1.28.4", "1.30.1")
	list["b/bad"] = &Image{k8sVersion: "b/bad"}
	list["a/bad"] = &Image{k8sVersion: "a/bad"}
	list["c/bad"] = nil

	for i := 0; i < 50; i++ {
		err := checkImageList("test list", list)
		if err == nil || !strings.Contains(err.Error(), `"a/bad"`) {
			t.Fatalf("want an error naming \"a/bad\", got %v", err)
		}
	}
}

// A downloaded list may hold a JSON null, which decodes to a nil *Image.
func TestCheckImageListRejectsJSONNullEntry(t *testing.T) {
	list := map[string]*Image{}
	if err := json.Unmarshal([]byte(`{"1.30.1":null}`), &list); err != nil {
		t.Fatal(err)
	}

	if err := checkImageList("test list", list); err == nil {
		t.Fatal("a null entry should be rejected")
	}
}

// Both a key and an entry version come from a file, so a huge one must not
// be copied whole into an error message.
func TestCheckImageListShortensLongNames(t *testing.T) {
	huge := strings.Repeat("x", 100000) + "/"

	cases := map[string]map[string]*Image{
		"key":           {huge: {k8sVersion: huge}},
		"entry version": {"1.30.1": {k8sVersion: huge}},
	}

	for name, list := range cases {
		err := checkImageList("test list", list)
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}
		if len(err.Error()) > 1000 {
			t.Errorf("%s: error is %d bytes long", name, len(err.Error()))
		}
	}
}

func TestShorten(t *testing.T) {
	if got := shorten("1.30.1"); got != `"1.30.1"` {
		t.Errorf("short string: got %s", got)
	}

	exact := strings.Repeat("a", 2*maxVersionLength)
	if got := shorten(exact); got != `"`+exact+`"` {
		t.Errorf("a string at the limit must not be cut: got %s", got)
	}

	over := exact + "a"
	if got := shorten(over); got != `"`+exact+`"...` {
		t.Errorf("a string over the limit must be cut: got %s", got)
	}
}

// When an entry is for a different version than its key, the error says which
// version the entry is for, as that is what a person must look for in the file.
func TestCheckImageListMismatchNamesBothVersions(t *testing.T) {
	err := checkImageList("test list", map[string]*Image{"1.30.1": {k8sVersion: "1.30.2"}})
	if err == nil {
		t.Fatal("expected an error")
	}

	for _, want := range []string{`"1.30.1"`, `"1.30.2"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %s", err, want)
		}
	}
}
