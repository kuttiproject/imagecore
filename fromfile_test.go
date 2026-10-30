package imagecore

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kuttiproject/drivercore"
)

var longAgo = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

// setOld gives a file a modification time long ago, so that a later change to
// it shows.
func setOld(t *testing.T, path string) {
	t.Helper()

	if err := os.Chtimes(path, longAgo, longAgo); err != nil {
		t.Fatal(err)
	}
}

func isOld(t *testing.T, path string) bool {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	return info.ModTime().Equal(longAgo)
}

func TestFromFileCachesTheImage(t *testing.T) {
	e := newImageEnv(t, nil)
	content := "the image"
	e.put(
		entry(testVersion, sum(content), "u", "Notdownloaded", false),
		entry("1.31.0", "other", "u2", "Notdownloaded", false),
	)
	img := e.image(testVersion)

	if err := img.FromFile(e.source(content)); err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, e.cached(testVersion)); got != content {
		t.Errorf("cached file holds %q", got)
	}
	if img.Status() != drivercore.ImageStatusDownloaded {
		t.Errorf("image status is %q", img.Status())
	}
	if e.statusOf(testVersion) != drivercore.ImageStatusDownloaded {
		t.Error("the list does not say Downloaded")
	}
	if e.statusOf("1.31.0") != drivercore.ImageStatusNotDownloaded {
		t.Error("another image was changed")
	}

	// With no Unpack, no work directory is made. Only the image is in the cache.
	e.wantCacheOnly("kutti-" + testVersion + ".qcow2")

	// The locks stay, empty, in the config directory, with the list.
	want := []string{"driver-qemu-images-1.30.1.lock", "driver-qemu-images.json", "driver-qemu-images.lock"}
	if got := dirNames(t, e.configdir); !reflect.DeepEqual(got, want) {
		t.Errorf("config directory has %v, want %v", got, want)
	}

}

func TestFromFileLogsTheAgreedMessages(t *testing.T) {
	e := newImageEnv(t, nil)
	e.put(entry(testVersion, sum("x"), "u", "Notdownloaded", false))
	logs := captureLog(t)

	if err := e.image(testVersion).FromFile(e.source("x")); err != nil {
		t.Fatal(err)
	}

	var info []string
	for _, line := range logs.lines {
		if strings.HasPrefix(line, "<2> ") {
			info = append(info, line)
		}
	}
	want := []string{"<2> Checking image validity...", "<2> Copying image to local cache..."}
	if !reflect.DeepEqual(info, want) {
		t.Errorf("info messages: got %q, want %q", info, want)
	}
}

func TestFromFileRejectsAWrongChecksum(t *testing.T) {
	e := newImageEnv(t, nil)
	e.put(entry(testVersion, sum("right"), "u", "Notdownloaded", false))
	img := e.image(testVersion)

	err := img.FromFile(e.source("wrong"))
	if err == nil || err.Error() != "file is not valid" {
		t.Fatalf("got %v", err)
	}

	e.wantCacheOnly()
	if img.Status() != drivercore.ImageStatusNotDownloaded || e.statusOf(testVersion) != drivercore.ImageStatusNotDownloaded {
		t.Error("the status must not change")
	}
}

func TestFromFileOfAMissingFile(t *testing.T) {
	e := newImageEnv(t, nil)
	e.put(entry(testVersion, sum("x"), "u", "Notdownloaded", false))

	err := e.image(testVersion).FromFile(filepath.Join(t.TempDir(), "nothing"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("got %v", err)
	}
	e.wantCacheOnly()
}

// Without an InUse function (vbox), a cached image is always replaced, and
// nothing asks whether it is in use.
func TestFromFileWithoutInUseAlwaysCopies(t *testing.T) {
	e := newImageEnv(t, nil)
	content := "the image"
	e.put(entry(testVersion, sum(content), "u", "Notdownloaded", false))

	// A cached file with the right content is copied over anyway.
	writeFile(t, e.cached(testVersion), content)
	setOld(t, e.cached(testVersion))
	if err := e.image(testVersion).FromFile(e.source(content)); err != nil {
		t.Fatal(err)
	}
	if isOld(t, e.cached(testVersion)) {
		t.Error("the file with the same content should have been copied again")
	}

	// A cached file with other content is replaced.
	writeFile(t, e.cached(testVersion), "old content")
	if err := e.image(testVersion).FromFile(e.source(content)); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, e.cached(testVersion)); got != content {
		t.Errorf("cached file holds %q", got)
	}
}

type inUseHook struct {
	mu    sync.Mutex
	calls []string
	inuse bool
	err   error
}

func (h *inUseHook) fn(version string) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.calls = append(h.calls, version)
	return h.inuse, h.err
}

func (h *inUseHook) called() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.calls...)
}

func TestFromFileWithInUseSkipsACopyWithTheSameChecksum(t *testing.T) {
	hook := &inUseHook{inuse: true} // would refuse, if asked
	e := newImageEnv(t, func(c *Config) { c.InUse = hook.fn })
	content := "the image"
	e.put(entry(testVersion, sum(content), "u", "Notdownloaded", false))

	writeFile(t, e.cached(testVersion), content)
	setOld(t, e.cached(testVersion))
	img := e.image(testVersion)

	if err := img.FromFile(e.source(content)); err != nil {
		t.Fatal(err)
	}

	if !isOld(t, e.cached(testVersion)) {
		t.Error("the cached file must not be copied again")
	}
	if len(hook.called()) != 0 {
		t.Errorf("InUse must not be asked: %v", hook.called())
	}
	// It is still marked Downloaded, which is why importing an image that is
	// already there is useful.
	if img.Status() != drivercore.ImageStatusDownloaded || e.statusOf(testVersion) != drivercore.ImageStatusDownloaded {
		t.Error("the image must be marked Downloaded")
	}
}

func TestFromFileWithInUseReplacesAnImageThatIsNotInUse(t *testing.T) {
	hook := &inUseHook{}
	e := newImageEnv(t, func(c *Config) { c.InUse = hook.fn })
	content := "the image"
	e.put(entry(testVersion, sum(content), "u", "Notdownloaded", false))
	writeFile(t, e.cached(testVersion), "old content")

	if err := e.image(testVersion).FromFile(e.source(content)); err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, e.cached(testVersion)); got != content {
		t.Errorf("cached file holds %q", got)
	}
	if !reflect.DeepEqual(hook.called(), []string{testVersion}) {
		t.Errorf("InUse calls: %v", hook.called())
	}
}

func TestFromFileWithInUseRefusesToReplaceAnImageInUse(t *testing.T) {
	hook := &inUseHook{inuse: true}
	e := newImageEnv(t, func(c *Config) { c.InUse = hook.fn })
	e.put(entry(testVersion, sum("new"), "u", "Notdownloaded", false))
	writeFile(t, e.cached(testVersion), "old content")
	img := e.image(testVersion)

	err := img.FromFile(e.source("new"))

	want := "cannot replace cached image for K8s version 1.30.1: it is in use as a backing disk by one or more machines"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %s", err, want)
	}
	if got := readFile(t, e.cached(testVersion)); got != "old content" {
		t.Errorf("the cached file was changed to %q", got)
	}
	if img.Status() != drivercore.ImageStatusNotDownloaded || e.statusOf(testVersion) != drivercore.ImageStatusNotDownloaded {
		t.Error("the status must not change")
	}
}

func TestFromFileWithInUseFailsClosedIfItCannotTell(t *testing.T) {
	hook := &inUseHook{err: errors.New("qemu-img is missing")}
	e := newImageEnv(t, func(c *Config) { c.InUse = hook.fn })
	e.put(entry(testVersion, sum("new"), "u", "Notdownloaded", false))
	writeFile(t, e.cached(testVersion), "old content")

	err := e.image(testVersion).FromFile(e.source("new"))

	want := "cannot replace cached image for K8s version 1.30.1: could not verify it is unused: qemu-img is missing"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %s", err, want)
	}
	if got := readFile(t, e.cached(testVersion)); got != "old content" {
		t.Errorf("the cached file was changed to %q", got)
	}
}

// With nothing cached, nothing can be using it, so InUse is not asked.
func TestFromFileWithInUseDoesNotAskIfNothingIsCached(t *testing.T) {
	hook := &inUseHook{inuse: true}
	e := newImageEnv(t, func(c *Config) { c.InUse = hook.fn })
	e.put(entry(testVersion, sum("new"), "u", "Notdownloaded", false))

	if err := e.image(testVersion).FromFile(e.source("new")); err != nil {
		t.Fatal(err)
	}
	if len(hook.called()) != 0 {
		t.Errorf("InUse must not be asked: %v", hook.called())
	}
	if got := readFile(t, e.cached(testVersion)); got != "new" {
		t.Errorf("cached file holds %q", got)
	}
}

func TestFromFileWithUnpack(t *testing.T) {
	var (
		gotSrc, gotWork string
		workdirExisted  bool
		cleanups        int
		cachedAtCleanup bool
	)

	var e *imageEnv
	e = newImageEnv(t, func(c *Config) {
		c.Unpack = func(path, workdir string) (string, func(), error) {
			gotSrc, gotWork = path, workdir
			info, err := os.Stat(workdir)
			workdirExisted = err == nil && info.IsDir()

			out, _, err := zipUnpack(path, workdir)
			return out, func() {
				cleanups++
				_, statErr := os.Stat(e.cached(testVersion))
				cachedAtCleanup = statErr == nil
			}, err
		}
	})

	inner := "the unpacked image"
	e.put(entry(testVersion, sum(inner), "u", "Notdownloaded", false))

	archive := e.source(makeZip(t, "disk.img", inner))
	if err := e.image(testVersion).FromFile(archive); err != nil {
		t.Fatal(err)
	}

	// The checksum is of the unpacked file, which is what is cached.
	if got := readFile(t, e.cached(testVersion)); got != inner {
		t.Errorf("cached file holds %q", got)
	}
	if e.statusOf(testVersion) != drivercore.ImageStatusDownloaded {
		t.Error("not marked Downloaded")
	}

	if gotSrc != archive {
		t.Errorf("Unpack got %q, want %q", gotSrc, archive)
	}
	if filepath.Dir(gotWork) != e.cachedir || !strings.HasPrefix(filepath.Base(gotWork), "kutti-k8s-1.30.1.") ||
		!strings.HasSuffix(gotWork, ".work") {
		t.Errorf("work directory %q is not a work directory in the cache", gotWork)
	}
	if !workdirExisted {
		t.Error("the work directory must exist when Unpack is called")
	}
	if cleanups != 1 || !cachedAtCleanup {
		t.Errorf("cleanup called %d times; cached file present at the time: %t", cleanups, cachedAtCleanup)
	}

	// The work directory and everything in it is gone.
	e.wantCacheOnly("kutti-" + testVersion + ".qcow2")
}

func TestFromFileUnpackFailures(t *testing.T) {
	boom := errors.New("cannot unpack this")

	cases := map[string]struct {
		unpack func(string, string) (string, func(), error)
		want   string
	}{
		"error":          {func(string, string) (string, func(), error) { return "", nil, boom }, "cannot unpack this"},
		"no file":        {func(string, string) (string, func(), error) { return "", nil, nil }, "Unpack returned no file"},
		"missing output": {func(string, string) (string, func(), error) { return "/nonexistent/file", nil, nil }, ""},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cleanups := 0
			e := newImageEnv(t, func(c *Config) {
				c.Unpack = func(p, w string) (string, func(), error) {
					out, _, err := tc.unpack(p, w)
					return out, func() { cleanups++ }, err
				}
			})
			e.put(entry(testVersion, sum("x"), "u", "Notdownloaded", false))

			err := e.image(testVersion).FromFile(e.source("x"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want it to contain %q", err, tc.want)
			}
			if name == "error" && err != boom {
				t.Errorf("the error from Unpack must be returned as it is, got %v", err)
			}

			if cleanups != 1 {
				t.Errorf("cleanup called %d times, want 1 even on failure", cleanups)
			}
			e.wantCacheOnly()
			if e.statusOf(testVersion) != drivercore.ImageStatusNotDownloaded {
				t.Error("the status must not change")
			}
		})
	}
}

// The checksum is of what Unpack returns. A matching archive is not enough.
func TestFromFileChecksIfTheUnpackedFileNotTheArchive(t *testing.T) {
	e := newImageEnv(t, func(c *Config) { c.Unpack = zipUnpack })
	inner := "inner"
	archive := makeZip(t, "disk.img", inner)

	// The entry has the checksum of the archive, not of its content.
	e.put(entry(testVersion, sum(archive), "u", "Notdownloaded", false))

	err := e.image(testVersion).FromFile(e.source(archive))
	if err == nil || err.Error() != "file is not valid" {
		t.Fatalf("got %v", err)
	}
	e.wantCacheOnly()
}

func TestFromFileFailsIfTheListHasChanged(t *testing.T) {
	e := newImageEnv(t, nil)
	e.put(entry(testVersion, sum("x"), "u", "Notdownloaded", false))
	img := e.image(testVersion)
	src := e.source("x")

	cases := map[string]string{
		"checksum changed": list(entry(testVersion, "other", "u", "Notdownloaded", false)),
		"URL changed":      list(entry(testVersion, sum("x"), "u2", "Notdownloaded", false)),
		"entry removed":    list(entry("1.31.0", "c", "u", "Notdownloaded", false)),
		"empty list":       `{}`,
	}

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			writeFile(t, e.listpath, content)

			err := img.FromFile(src)
			if err == nil || !strings.Contains(err.Error(), "has changed or been removed in the image list") ||
				!strings.Contains(err.Error(), "Kubernetes "+testVersion) {
				t.Fatalf("got %v", err)
			}

			e.wantCacheOnly()
			if readFile(t, e.listpath) != content {
				t.Error("the list must not be changed")
			}
		})
	}
}

func TestFromFileFailsIfTheListIsUnusable(t *testing.T) {
	e := newImageEnv(t, nil)
	e.put(entry(testVersion, sum("x"), "u", "Notdownloaded", false))
	img := e.image(testVersion)

	writeFile(t, e.listpath, `{bad`)

	err := img.FromFile(e.source("x"))
	var invalid *invalidListError
	if !errors.As(err, &invalid) {
		t.Fatalf("got %v", err)
	}
	e.wantCacheOnly()
	if readFile(t, e.listpath) != `{bad` || len(corruptFiles(t, e.listpath)) != 0 {
		t.Error("the unusable list must be left alone")
	}
}

// The list changing while the file is copied is reported, and the status is
// not set, because it would be for the wrong image.
func TestFromFileReportsTheListChangingDuringTheImport(t *testing.T) {
	var e *imageEnv
	e = newImageEnv(t, func(c *Config) {
		// Unpack runs after the list was checked and before it is updated.
		c.Unpack = func(path, workdir string) (string, func(), error) {
			writeFile(t, e.listpath, list(entry(testVersion, "new checksum", "u", "Notdownloaded", false)))
			return path, nil, nil
		}
	})
	e.put(entry(testVersion, sum("x"), "u", "Notdownloaded", false))
	img := e.image(testVersion)

	err := img.FromFile(e.source("x"))
	if err == nil || !strings.Contains(err.Error(), "changed while the image for Kubernetes 1.30.1 was being added") {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "in the cache, but is not marked as downloaded") {
		t.Errorf("the error should say what state things are in: %v", err)
	}

	if got := readFile(t, e.cached(testVersion)); got != "x" {
		t.Errorf("cached file holds %q", got)
	}
	if img.Status() != drivercore.ImageStatusNotDownloaded || e.statusOf(testVersion) != drivercore.ImageStatusNotDownloaded {
		t.Error("the status must not change")
	}
}

func TestFromFileWaitsForTheVersionLock(t *testing.T) {
	e := newImageEnv(t, nil)
	e.put(entry(testVersion, sum("x"), "u", "Notdownloaded", false))
	logs := captureLog(t)
	img := e.image(testVersion)
	src := e.source("x")

	unlock := holdLock(t, e.versionLock(testVersion))

	done := make(chan error, 1)
	go func() { done <- img.FromFile(src) }()

	select {
	case err := <-done:
		unlock()
		t.Fatalf("FromFile did not wait for the lock (returned %v)", err)
	case <-time.After(400 * time.Millisecond):
	}
	e.wantCacheOnly()

	unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("FromFile did not finish after the lock was released")
	}

	waits := 0
	for _, line := range logs.lines {
		if line == "<2> Waiting for another kutti process to finish with the image for Kubernetes 1.30.1..." {
			waits++
		}
	}
	if waits != 1 {
		t.Errorf("the wait should be logged once, was logged %d times: %q", waits, logs.lines)
	}
}

// The lock is for one version. Another version is not held up.
func TestVersionLocksAreIndependent(t *testing.T) {
	e := newImageEnv(t, nil)
	e.put(
		entry(testVersion, sum("x"), "u", "Notdownloaded", false),
		entry("1.31.0", sum("y"), "u", "Notdownloaded", false),
	)

	unlock := holdLock(t, e.versionLock(testVersion))
	defer unlock()

	done := make(chan error, 1)
	other := e.image("1.31.0")
	src := e.source("y")
	go func() { done <- other.FromFile(src) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a lock on one version held up another")
	}
}

func TestFromFileReleasesTheVersionLock(t *testing.T) {
	e := newImageEnv(t, nil)
	e.put(entry(testVersion, sum("x"), "u", "Notdownloaded", false))

	// A failure, then a success: the lock must be free after each.
	if err := e.image(testVersion).FromFile(e.source("wrong")); err == nil {
		t.Fatal("expected an error")
	}
	unlock, err := lockWithTimeout(e.versionLock(testVersion), 500*time.Millisecond)
	if err != nil {
		t.Fatalf("the lock was not released after a failure: %v", err)
	}
	unlock()

	if err := e.image(testVersion).FromFile(e.source("x")); err != nil {
		t.Fatal(err)
	}
	unlock, err = lockWithTimeout(e.versionLock(testVersion), 500*time.Millisecond)
	if err != nil {
		t.Fatalf("the lock was not released after a success: %v", err)
	}
	unlock()
}

func TestLockVersionRefusesInvalidVersions(t *testing.T) {
	e := newImageEnv(t, nil)

	for _, v := range []string{"", "../x", "a/b", ".1"} {
		if unlock, err := e.store.lockVersion(v); err == nil {
			unlock()
			t.Errorf("lockVersion(%q) should fail", v)
		}
	}
	if names := dirNames(t, e.configdir); len(names) != 0 {
		t.Errorf("no lock file may be made for an invalid version, found %v", names)
	}
}
