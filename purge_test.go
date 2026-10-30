package imagecore

import (
	"encoding/json"
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

// downloadedEnv is an imageEnv with the image for testVersion cached and
// listed as Downloaded.
func downloadedEnv(t *testing.T, modify func(*Config)) *imageEnv {
	t.Helper()

	e := newImageEnv(t, modify)
	e.put(entry(testVersion, sum("content"), "u", "Downloaded", false))
	writeFile(t, e.cached(testVersion), "content")

	return e
}

func TestPurgeLocalRemovesTheCachedFile(t *testing.T) {
	e := downloadedEnv(t, nil)
	e.put(
		entry(testVersion, sum("content"), "u", "Downloaded", false),
		entry("1.31.0", "c", "u2", "Downloaded", false),
	)
	img := e.image(testVersion)
	logs := captureLog(t)

	if err := img.PurgeLocal(); err != nil {
		t.Fatal(err)
	}

	e.wantCacheOnly()
	if img.Status() != drivercore.ImageStatusNotDownloaded || e.statusOf(testVersion) != drivercore.ImageStatusNotDownloaded {
		t.Error("the image must be Notdownloaded, in memory and in the list")
	}
	if e.statusOf("1.31.0") != drivercore.ImageStatusDownloaded {
		t.Error("another image was changed")
	}

	var info []string
	for _, line := range logs.lines {
		if strings.HasPrefix(line, "<2> ") {
			info = append(info, line)
		}
	}
	if want := []string{"<2> Purging cached image for Kubernetes 1.30.1..."}; !reflect.DeepEqual(info, want) {
		t.Errorf("info messages: got %q, want %q", info, want)
	}
}

// The status spelling written is the constant of drivercore.
func TestPurgeLocalWritesTheDrivercoreStatus(t *testing.T) {
	e := downloadedEnv(t, nil)

	if err := e.image(testVersion).PurgeLocal(); err != nil {
		t.Fatal(err)
	}

	want := list(entry(testVersion, sum("content"), "u", string(drivercore.ImageStatusNotDownloaded), false))
	if got := readFile(t, e.listpath); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestPurgeLocalDoesNothingIfTheImageIsNotDownloaded(t *testing.T) {
	e := newImageEnv(t, nil)
	e.put(entry(testVersion, sum("content"), "u", "Notdownloaded", false))
	// A file that is there but not listed is not touched.
	writeFile(t, e.cached(testVersion), "content")
	before := readFile(t, e.listpath)
	logs := captureLog(t)

	if err := e.image(testVersion).PurgeLocal(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(e.cached(testVersion)); err != nil {
		t.Error("the file must not be removed")
	}
	if readFile(t, e.listpath) != before {
		t.Error("the list must not change")
	}
	for _, line := range logs.lines {
		if strings.Contains(line, "Purging") {
			t.Errorf("nothing is purged, but logged %q", line)
		}
	}
}

func TestPurgeLocalDoesNothingIfTheImageIsNotInTheList(t *testing.T) {
	e := downloadedEnv(t, nil)
	img := e.image(testVersion)
	writeFile(t, e.listpath, `{}`)

	if err := img.PurgeLocal(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.cached(testVersion)); err != nil {
		t.Error("the file must not be removed")
	}
	if readFile(t, e.listpath) != `{}` {
		t.Error("the list must not change")
	}
}

// A cached file that has already gone is not an error. The old drivers failed,
// and left the status at Downloaded for good.
func TestPurgeLocalSucceedsIfTheFileIsAlreadyGone(t *testing.T) {
	e := downloadedEnv(t, nil)
	if err := os.Remove(e.cached(testVersion)); err != nil {
		t.Fatal(err)
	}
	img := e.image(testVersion)

	if err := img.PurgeLocal(); err != nil {
		t.Fatal(err)
	}

	if img.Status() != drivercore.ImageStatusNotDownloaded || e.statusOf(testVersion) != drivercore.ImageStatusNotDownloaded {
		t.Error("the status must be put right")
	}
}

func TestPurgeLocalReportsAFileThatCannotBeRemoved(t *testing.T) {
	e := downloadedEnv(t, nil)

	// A directory with something in it cannot be removed as a file.
	if err := os.Remove(e.cached(testVersion)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(e.cached(testVersion), "x"), 0o777); err != nil {
		t.Fatal(err)
	}
	img := e.image(testVersion)

	err := img.PurgeLocal()
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("got %v", err)
	}

	if img.Status() != drivercore.ImageStatusDownloaded || e.statusOf(testVersion) != drivercore.ImageStatusDownloaded {
		t.Error("the status must not change")
	}
}

func TestPurgeLocalWithInUseRefusesToRemoveAnImageInUse(t *testing.T) {
	hook := &inUseHook{inuse: true}
	e := downloadedEnv(t, func(c *Config) { c.InUse = hook.fn })
	img := e.image(testVersion)

	err := img.PurgeLocal()

	want := "cannot remove cached image for K8s version 1.30.1: it is in use as a backing disk by one or more machines"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %s", err, want)
	}
	if got := readFile(t, e.cached(testVersion)); got != "content" {
		t.Errorf("the file was changed: %q", got)
	}
	if img.Status() != drivercore.ImageStatusDownloaded || e.statusOf(testVersion) != drivercore.ImageStatusDownloaded {
		t.Error("the status must not change")
	}
	if !reflect.DeepEqual(hook.called(), []string{testVersion}) {
		t.Errorf("InUse calls: %v", hook.called())
	}
}

func TestPurgeLocalWithInUseFailsClosedIfItCannotTell(t *testing.T) {
	hook := &inUseHook{err: errors.New("qemu-img is missing")}
	e := downloadedEnv(t, func(c *Config) { c.InUse = hook.fn })

	err := e.image(testVersion).PurgeLocal()

	want := "cannot remove cached image for K8s version 1.30.1: could not verify it is unused: qemu-img is missing"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %s", err, want)
	}
	if got := readFile(t, e.cached(testVersion)); got != "content" {
		t.Errorf("the file was changed: %q", got)
	}
}

func TestPurgeLocalWithInUseRemovesAnImageThatIsNotInUse(t *testing.T) {
	hook := &inUseHook{}
	e := downloadedEnv(t, func(c *Config) { c.InUse = hook.fn })

	if err := e.image(testVersion).PurgeLocal(); err != nil {
		t.Fatal(err)
	}
	e.wantCacheOnly()
	if !reflect.DeepEqual(hook.called(), []string{testVersion}) {
		t.Errorf("InUse calls: %v", hook.called())
	}
}

// What counts is the list as it is when PurgeLocal runs, not the status the
// Image had when it was got.
func TestPurgeLocalUsesTheCurrentList(t *testing.T) {
	t.Run("purged by someone else", func(t *testing.T) {
		e := downloadedEnv(t, nil)
		img := e.image(testVersion)

		e.put(entry(testVersion, sum("content"), "u", "Notdownloaded", false))

		if err := img.PurgeLocal(); err != nil {
			t.Fatal(err)
		}
		if img.Status() != drivercore.ImageStatusNotDownloaded {
			t.Error("the image should now be Notdownloaded")
		}
		// Nothing is listed as Downloaded, so nothing is removed.
		if _, err := os.Stat(e.cached(testVersion)); err != nil {
			t.Error("the file must not be removed")
		}
	})

	t.Run("fetched by someone else", func(t *testing.T) {
		e := newImageEnv(t, nil)
		e.put(entry(testVersion, sum("content"), "u", "Notdownloaded", false))
		img := e.image(testVersion)

		e.put(entry(testVersion, sum("content"), "u", "Downloaded", false))
		writeFile(t, e.cached(testVersion), "content")

		if err := img.PurgeLocal(); err != nil {
			t.Fatal(err)
		}
		e.wantCacheOnly()
		if e.statusOf(testVersion) != drivercore.ImageStatusNotDownloaded {
			t.Error("the list must say Notdownloaded")
		}
	})

	t.Run("list updated with another image", func(t *testing.T) {
		// The update made the new entry Notdownloaded, which is what a purge
		// would write. The cached file is of the old image, and stays.
		e := downloadedEnv(t, nil)
		img := e.image(testVersion)

		newentry := entry(testVersion, "new checksum", "u", "Notdownloaded", false)
		e.put(newentry)

		if err := img.PurgeLocal(); err != nil {
			t.Fatal(err)
		}
		if readFile(t, e.listpath) != list(newentry) {
			t.Error("the new list must be as it was")
		}
	})
}

func TestPurgeLocalWaitsForTheVersionLock(t *testing.T) {
	e := downloadedEnv(t, nil)
	img := e.image(testVersion)

	unlock := holdLock(t, e.versionLock(testVersion))

	done := make(chan error, 1)
	go func() { done <- img.PurgeLocal() }()

	select {
	case err := <-done:
		unlock()
		t.Fatalf("PurgeLocal did not wait for the lock (returned %v)", err)
	case <-time.After(400 * time.Millisecond):
	}
	if _, err := os.Stat(e.cached(testVersion)); err != nil {
		t.Error("the file must not be removed while the lock is held")
	}

	unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PurgeLocal did not finish")
	}
	e.wantCacheOnly()
}

func TestPurgeLocalWithAnUnusableListFailsAndTouchesNothing(t *testing.T) {
	e := downloadedEnv(t, nil)
	img := e.image(testVersion)
	writeFile(t, e.listpath, `{bad`)

	err := img.PurgeLocal()
	var invalid *invalidListError
	if !errors.As(err, &invalid) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(e.cached(testVersion)); err != nil {
		t.Error("the file must not be removed")
	}
	if len(corruptFiles(t, e.listpath)) != 0 {
		t.Error("the list must be left alone")
	}
}

// A fetch, an import and a purge of the same version never overlap.
func TestOperationsOnOneVersionAreSerialized(t *testing.T) {
	var mu sync.Mutex
	running, maxRunning := 0, 0

	e := newImageEnv(t, func(c *Config) {
		c.Unpack = func(path, workdir string) (string, func(), error) {
			mu.Lock()
			running++
			if running > maxRunning {
				maxRunning = running
			}
			mu.Unlock()

			time.Sleep(40 * time.Millisecond)

			mu.Lock()
			running--
			mu.Unlock()
			return path, nil, nil
		}
	})
	e.put(entry(testVersion, sum("content"), "u", "Notdownloaded", false))

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		store := e.anotherStore()
		img, err := store.GetImage(testVersion)
		if err != nil {
			t.Fatal(err)
		}
		src := e.source("content")

		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := img.FromFile(src); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	if maxRunning != 1 {
		t.Errorf("%d imports ran at once, want 1", maxRunning)
	}
}

func TestSetStatusChangesOnlyOneImage(t *testing.T) {
	e := newImageEnv(t, nil)
	e.put(
		entry("1.29.0", "c29", "u29", "Downloaded", true),
		entry("1.30.1", "c30", "u30", "Notdownloaded", false),
		entry("1.31.0", "c31", "u31", "Notdownloaded", false),
	)

	if err := e.store.setStatus(e.image("1.30.1"), drivercore.ImageStatusDownloaded); err != nil {
		t.Fatal(err)
	}

	want := list(
		entry("1.29.0", "c29", "u29", "Downloaded", true),
		entry("1.30.1", "c30", "u30", "Downloaded", false),
		entry("1.31.0", "c31", "u31", "Notdownloaded", false),
	)
	if got := readFile(t, e.listpath); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestSetStatusDoesNotWriteIfNothingChanges(t *testing.T) {
	e := newImageEnv(t, nil)
	e.put(entry("1.30.1", "c30", "u30", "Downloaded", false))
	setOld(t, e.listpath)

	if err := e.store.setStatus(e.image("1.30.1"), drivercore.ImageStatusDownloaded); err != nil {
		t.Fatal(err)
	}
	if !isOld(t, e.listpath) {
		t.Error("the list must not be written")
	}
}

func TestSetStatusRefusesAnImageThatIsNotInTheListAnyMore(t *testing.T) {
	e := newImageEnv(t, nil)
	e.put(entry("1.30.1", "c30", "u30", "Notdownloaded", false))
	img := e.image("1.30.1")

	cases := map[string]string{
		"checksum changed": list(entry("1.30.1", "other", "u30", "Notdownloaded", false)),
		"URL changed":      list(entry("1.30.1", "c30", "other", "Notdownloaded", false)),
		"entry removed":    list(entry("1.31.0", "c30", "u30", "Notdownloaded", false)),
		"empty list":       `{}`,
	}

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			writeFile(t, e.listpath, content)

			err := e.store.setStatus(img, drivercore.ImageStatusDownloaded)
			if !errors.Is(err, errImageChanged) {
				t.Fatalf("got %v", err)
			}
			if readFile(t, e.listpath) != content {
				t.Error("the list must not be changed")
			}
		})
	}
}

func TestSetStatusLeavesAnUnusableListAlone(t *testing.T) {
	e := newImageEnv(t, nil)
	e.put(entry("1.30.1", "c30", "u30", "Notdownloaded", false))
	img := e.image("1.30.1")
	writeFile(t, e.listpath, `{bad`)

	err := e.store.setStatus(img, drivercore.ImageStatusDownloaded)
	var invalid *invalidListError
	if !errors.As(err, &invalid) || errors.Is(err, errImageChanged) {
		t.Fatalf("got %v", err)
	}
	if readFile(t, e.listpath) != `{bad` || len(corruptFiles(t, e.listpath)) != 0 {
		t.Error("the list must be left alone")
	}
}

func TestSetStatusWaitsForTheListLock(t *testing.T) {
	e := newImageEnv(t, nil)
	e.put(entry("1.30.1", "c30", "u30", "Notdownloaded", false))
	img := e.image("1.30.1")

	unlock := holdLock(t, filepath.Join(e.configdir, "driver-qemu-images.lock"))

	done := make(chan error, 1)
	go func() { done <- e.store.setStatus(img, drivercore.ImageStatusDownloaded) }()

	select {
	case err := <-done:
		unlock()
		t.Fatalf("setStatus did not wait for the lock (returned %v)", err)
	case <-time.After(400 * time.Millisecond):
	}
	if e.statusOf("1.30.1") != drivercore.ImageStatusNotDownloaded {
		t.Error("the list must not be written while the lock is held")
	}

	unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("setStatus did not finish")
	}
	if e.statusOf("1.30.1") != drivercore.ImageStatusDownloaded {
		t.Error("the list was not written")
	}
}

// Status changes made at the same time, by Stores that stand for different
// processes, must not undo each other. The old drivers saved their whole
// in-memory list, so the last to save won.
func TestConcurrentStatusChangesDoNotUndoEachOther(t *testing.T) {
	e := newImageEnv(t, nil)
	versions := []string{"1.28.0", "1.29.0", "1.30.0", "1.31.0"}

	var entries []string
	for _, v := range versions {
		entries = append(entries, entry(v, "c"+v, "u"+v, "Notdownloaded", false))
	}
	entries = append(entries, entry("1.99.0", "keep", "keep", "Downloaded", true))
	e.put(entries...)

	const rounds = 15

	var wg sync.WaitGroup
	for _, v := range versions {
		store := e.anotherStore()
		img, err := store.GetImage(v)
		if err != nil {
			t.Fatal(err)
		}
		concrete := img.(*Image)

		wg.Add(1)
		go func(store *Store, img *Image) {
			defer wg.Done()

			for i := 0; i < rounds; i++ {
				status := drivercore.ImageStatusDownloaded
				if i%2 == 1 {
					status = drivercore.ImageStatusNotDownloaded
				}
				if err := store.setStatus(img, status); err != nil {
					t.Error(err)
					return
				}
			}
		}(store, concrete)
	}
	wg.Wait()

	// 15 rounds, the last of them (index 14) is even: Downloaded.
	var got map[string]*Image
	if err := json.Unmarshal([]byte(readFile(t, e.listpath)), &got); err != nil {
		t.Fatal(err)
	}
	for _, v := range versions {
		if got[v] == nil || got[v].status != drivercore.ImageStatusDownloaded {
			t.Errorf("%s: lost an update: %+v", v, got[v])
		}
	}
	if keep := got["1.99.0"]; keep == nil || keep.status != drivercore.ImageStatusDownloaded || !keep.deprecated {
		t.Errorf("an entry nobody touched was changed: %+v", keep)
	}
	if names := dirNames(t, e.configdir); len(names) != 2 {
		t.Errorf("unexpected files in the config directory: %v", names)
	}
}

// The list changes while PurgeLocal works, after it has read the entry. The
// change was made by an update, which made the image Notdownloaded, so there
// is nothing to save, and nothing to report.
func TestPurgeLocalWhenTheListChangesUnderIt(t *testing.T) {
	var e *imageEnv
	newentry := entry(testVersion, "new checksum", "u", "Notdownloaded", false)

	e = downloadedEnv(t, func(c *Config) {
		c.InUse = func(string) (bool, error) {
			e.put(newentry) // runs after the entry was read, before the status is saved
			return false, nil
		}
	})
	img := e.image(testVersion)

	if err := img.PurgeLocal(); err != nil {
		t.Fatal(err)
	}

	e.wantCacheOnly()
	if readFile(t, e.listpath) != list(newentry) {
		t.Error("the list the update made must be left as it is")
	}
	if img.Status() != drivercore.ImageStatusNotDownloaded {
		t.Error("the image should be Notdownloaded")
	}
}

// The list becomes unusable while PurgeLocal works. The file is already gone
// by then, and the error says why the list was not updated.
func TestPurgeLocalWhenTheListBecomesUnusable(t *testing.T) {
	var e *imageEnv
	e = downloadedEnv(t, func(c *Config) {
		c.InUse = func(string) (bool, error) {
			writeFile(t, e.listpath, `{bad`)
			return false, nil
		}
	})
	img := e.image(testVersion)

	err := img.PurgeLocal()
	var invalid *invalidListError
	if !errors.As(err, &invalid) {
		t.Fatalf("got %v", err)
	}
	if img.Status() != drivercore.ImageStatusDownloaded {
		t.Error("the status must not change when it could not be saved")
	}
	if readFile(t, e.listpath) != `{bad` || len(corruptFiles(t, e.listpath)) != 0 {
		t.Error("the unusable list must be left alone")
	}
}
