package imagecore

import (
	"fmt"
	"sort"
)

// maxVersionLength is the longest version string accepted.
const maxVersionLength = 64

// validVersion reports whether v is acceptable as a Kubernetes version in an
// image list. A version must be 1 to 64 characters long, start with an ASCII
// letter or digit, and continue with ASCII letters, digits, '.', '_', '+' or
// '-'. That is, it must match ^[A-Za-z0-9][A-Za-z0-9._+-]*$ and be at most 64
// characters.
//
// This is stricter than Kubernetes version syntax needs to be, on purpose.
// Versions come from a downloaded file, and are used to build the names of
// cached image files, temporary files and lock files. Nothing that could
// change the meaning of a path, such as a separator, a drive prefix or a
// leading dot, may get through.
func validVersion(v string) bool {
	if v == "" || len(v) > maxVersionLength || !isAlphaNumeric(v[0]) {
		return false
	}

	for i := 1; i < len(v); i++ {
		c := v[i]
		if !isAlphaNumeric(c) && c != '.' && c != '_' && c != '+' && c != '-' {
			return false
		}
	}

	return true
}

// checkImageList verifies an image list, as read from a downloaded file or
// from the local copy. Every key must be a valid version, every entry must be
// present, and the version recorded in an entry must be the key it is stored
// under. One bad entry makes the whole list invalid, because a list that is
// partly wrong cannot be trusted to be right elsewhere.
//
// source says where the list came from, and appears in the error. Entries are
// checked in key order, so the entry named in the error does not vary between
// runs.
func checkImageList(source string, list map[string]*Image) error {
	versions := make([]string, 0, len(list))
	for v := range list {
		versions = append(versions, v)
	}
	sort.Strings(versions)

	for _, v := range versions {
		if err := checkImageListEntry(v, list[v]); err != nil {
			return fmt.Errorf("imagecore: %s has an invalid entry for version %s: %v",
				source, shorten(v), err)
		}
	}

	return nil
}

func checkImageListEntry(key string, img *Image) error {
	switch {
	case !validVersion(key):
		return fmt.Errorf("the version is not valid")
	case img == nil:
		return fmt.Errorf("the entry is empty")
	case img.k8sVersion != key:
		return fmt.Errorf("the entry is for version %s", shorten(img.k8sVersion))
	}

	return nil
}

// shorten quotes s for use in a message, and cuts it if it is very long,
// because the strings come from a file that may be hostile or corrupt.
func shorten(s string) string {
	const limit = 2 * maxVersionLength

	if len(s) > limit {
		return fmt.Sprintf("%q...", s[:limit])
	}

	return fmt.Sprintf("%q", s)
}
