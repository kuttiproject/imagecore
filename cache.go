package imagecore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kuttiproject/kuttilog"
	"github.com/kuttiproject/workspace"
)

// staleWorkDirAge is how old a work directory must be before a fetch of the
// same version removes it, as the leftover of a process that was killed. It is
// a variable so that tests can change it.
var staleWorkDirAge = 24 * time.Hour

// copyBufferSize is the chunk size asked of workspace.CopyFile, which uses at
// least 4 MiB whatever is asked.
const copyBufferSize = 4 << 20

// ImagePath returns the path where the cached file for a version of
// Kubernetes is, or would be: "kutti-<version><ImageExtension>" in the cache
// directory. The file may not exist. A driver uses this to find the image from
// which to create machines, and in its InUse function, so that the name is
// worked out in one place only.
//
// It returns an error if the version is not a valid version, so that a path
// can never be made from something that is not.
func (s *Store) ImagePath(k8sversion string) (string, error) {
	if err := s.config.validate(); err != nil {
		return "", err
	}
	if !validVersion(k8sversion) {
		return "", fmt.Errorf("imagecore: %s is not a valid version", shorten(k8sversion))
	}

	dir, err := s.config.CacheDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, "kutti-"+k8sversion+s.config.ImageExtension), nil
}

// lockVersion takes the lock that serializes the fetch, import and purge of
// the image for one version between processes of the same user, and returns
// the function that releases it. It waits as long as it takes, because
// another process may be downloading a very large file. The lock file is in
// the config directory.
func (s *Store) lockVersion(k8sversion string) (func(), error) {
	listpath, err := s.listPath()
	if err != nil {
		return nil, err
	}
	if !validVersion(k8sversion) {
		return nil, fmt.Errorf("imagecore: %s is not a valid version", shorten(k8sversion))
	}

	lockpath := filepath.Join(filepath.Dir(listpath), s.config.DriverName+"-images-"+k8sversion+".lock")

	unlock, err := workspace.LockFile(lockpath, 0, func() {
		kuttilog.Printf(kuttilog.Info, "Waiting for another kutti process to finish with the image for Kubernetes %s...", k8sversion)
	})
	if err != nil {
		return nil, fmt.Errorf("imagecore: could not lock the image for Kubernetes %s: %w", k8sversion, err)
	}

	return unlock, nil
}

// newWorkDir creates a directory, in the cache directory, for the files made
// while fetching or importing one version. Its name is unique, so that a
// process of another user, working on the same version in a shared cache,
// cannot interfere.
func newWorkDir(cachedir string, k8sversion string) (string, error) {
	return os.MkdirTemp(cachedir, "kutti-k8s-"+k8sversion+".*.work")
}

// isWorkDirOf reports whether a name is that of a work directory made by
// newWorkDir for exactly this version. The part between the version and
// ".work" is the random number, which is all digits, so work directories of
// "1.3.1" are not taken for those of "1.3".
func isWorkDirOf(name string, k8sversion string) bool {
	prefix := "kutti-k8s-" + k8sversion + "."
	const suffix = ".work"

	if len(name) <= len(prefix)+len(suffix) ||
		!strings.HasPrefix(name, prefix) ||
		!strings.HasSuffix(name, suffix) {
		return false
	}

	for _, c := range []byte(name[len(prefix) : len(name)-len(suffix)]) {
		if !isDigit(c) {
			return false
		}
	}

	return true
}

// removeStaleWorkDirs removes the work directories for a version that are
// older than staleWorkDirAge. A process that is killed while downloading
// leaves its work directory behind, with a large partial file in it. Only old
// ones are removed because the cache may be shared with other users, whose
// work is not covered by this user's locks. Failures are ignored, as this is
// only housekeeping.
func removeStaleWorkDirs(cachedir string, k8sversion string) {
	entries, err := os.ReadDir(cachedir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() || !isWorkDirOf(entry.Name(), k8sversion) {
			continue
		}

		info, err := entry.Info()
		if err != nil || time.Since(info.ModTime()) <= staleWorkDirAge {
			continue
		}

		dir := filepath.Join(cachedir, entry.Name())
		kuttilog.Printf(kuttilog.Debug, "Removing stale work directory %s", dir)
		os.RemoveAll(dir)
	}
}
