package imagecore

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kuttiproject/drivercore"
)

// fetchEnv is an imageEnv with a server that has the image for testVersion.
func fetchEnv(t *testing.T, content string, modify func(*Config)) (*imageEnv, *imageServer) {
	t.Helper()

	e := newImageEnv(t, modify)
	srv := newImageServer(t)
	srv.put("/"+testVersion, content)
	e.put(entry(testVersion, sum(content), srv.url("/"+testVersion), "Notdownloaded", false))

	return e, srv
}

func TestFetchDownloadsAndCachesTheImage(t *testing.T) {
	content := strings.Repeat("image data ", 50000)
	e, srv := fetchEnv(t, content, nil)
	img := e.image(testVersion)
	logs := captureLog(t)

	if err := img.Fetch(); err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, e.cached(testVersion)); got != content {
		t.Errorf("cached file differs (%d bytes, want %d)", len(got), len(content))
	}
	if img.Status() != drivercore.ImageStatusDownloaded || e.statusOf(testVersion) != drivercore.ImageStatusDownloaded {
		t.Error("the image must be marked Downloaded, in memory and in the list")
	}
	if srv.requests("/"+testVersion) != 1 {
		t.Errorf("want one download, got %d", srv.requests("/"+testVersion))
	}

	// The work directory, with the download in it, is gone.
	e.wantCacheOnly("kutti-" + testVersion + ".qcow2")

	var info []string
	for _, line := range logs.lines {
		if strings.HasPrefix(line, "<2> ") {
			info = append(info, line)
		}
	}
	want := []string{
		"<2> Downloading image for Kubernetes 1.30.1...",
		"<2> Checking image validity...",
		"<2> Copying image to local cache...",
		"<2> Finished downloading image for Kubernetes 1.30.1.",
	}
	if !reflect.DeepEqual(info, want) {
		t.Errorf("info messages: got %q, want %q", info, want)
	}
}

func TestFetchWithProgressReportsTheDownload(t *testing.T) {
	content := strings.Repeat("0123456789", 300000) // 3 MB
	e, _ := fetchEnv(t, content, nil)

	var mu sync.Mutex
	var calls int
	var last, total int64
	monotonic := true

	err := e.image(testVersion).FetchWithProgress(func(current int64, tot int64) {
		mu.Lock()
		defer mu.Unlock()

		calls++
		if current < last {
			monotonic = false
		}
		last, total = current, tot
	})
	if err != nil {
		t.Fatal(err)
	}

	// The last report may be a little short of the total: workspace does not
	// report the bytes that arrive together with the end of the file.
	if calls == 0 || !monotonic || last <= 0 || last > total || total != int64(len(content)) {
		t.Errorf("progress: %d calls, monotonic %t, last %d, total %d, want a total of %d",
			calls, monotonic, last, total, len(content))
	}
	if e.statusOf(testVersion) != drivercore.ImageStatusDownloaded {
		t.Error("not marked Downloaded")
	}
}

func TestFetchFailedDownloadLeavesNothingBehind(t *testing.T) {
	e, srv := fetchEnv(t, "content", nil)
	srv.status = http.StatusInternalServerError
	img := e.image(testVersion)
	before := readFile(t, e.listpath)

	// It is the failed download that is reported, not what follows from it.
	err := img.Fetch()
	if err == nil || strings.Contains(err.Error(), "file is not valid") || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want a download error, got %v", err)
	}

	e.wantCacheOnly()
	if readFile(t, e.listpath) != before || img.Status() != drivercore.ImageStatusNotDownloaded {
		t.Error("nothing may change")
	}

	// A missing file is the same.
	srv.status = http.StatusOK
	srv.mu.Lock()
	delete(srv.files, "/"+testVersion)
	srv.mu.Unlock()
	err = img.Fetch()
	if err == nil || strings.Contains(err.Error(), "file is not valid") || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want a download error, got %v", err)
	}
	e.wantCacheOnly()
}

func TestFetchRejectsAWrongChecksum(t *testing.T) {
	e, srv := fetchEnv(t, "right", nil)
	srv.put("/"+testVersion, "something else")
	img := e.image(testVersion)

	err := img.Fetch()
	if err == nil || err.Error() != "file is not valid" {
		t.Fatalf("got %v", err)
	}

	e.wantCacheOnly()
	if img.Status() != drivercore.ImageStatusNotDownloaded || e.statusOf(testVersion) != drivercore.ImageStatusNotDownloaded {
		t.Error("the status must not change")
	}
}

func TestFetchUnpacksADownloadedArchive(t *testing.T) {
	inner := "the unpacked image"
	archive := makeZip(t, "disk.img", inner)

	var gotSrc, gotWork string
	e := newImageEnv(t, func(c *Config) {
		c.DownloadSuffix = ".download.zip"
		c.ImageExtension = ".vhdx"
		c.Unpack = func(path, workdir string) (string, func(), error) {
			gotSrc, gotWork = path, workdir
			return zipUnpack(path, workdir)
		}
	})
	srv := newImageServer(t)
	srv.put("/"+testVersion, archive)
	e.put(entry(testVersion, sum(inner), srv.url("/"+testVersion), "Notdownloaded", false))

	if err := e.image(testVersion).Fetch(); err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, e.cached(testVersion)); got != inner {
		t.Errorf("cached file holds %q", got)
	}

	// The download is in the work directory, which is given to Unpack, and has
	// the name the driver's DownloadSuffix makes.
	if filepath.Dir(gotSrc) != gotWork || filepath.Base(gotSrc) != "kutti-k8s-1.30.1.download.zip" {
		t.Errorf("Unpack got %q in %q", gotSrc, gotWork)
	}
	if filepath.Dir(gotWork) != e.cachedir {
		t.Errorf("work directory %q is not in the cache", gotWork)
	}

	e.wantCacheOnly("kutti-" + testVersion + ".vhdx")
}

// An image listed as Downloaded is downloaded again by Fetch, which repairs a
// cache from which the file has gone.
func TestFetchOfAnImageListedAsDownloadedDownloadsAgain(t *testing.T) {
	e, srv := fetchEnv(t, "content", nil)
	e.put(entry(testVersion, sum("content"), srv.url("/"+testVersion), "Downloaded", false))
	img := e.image(testVersion)
	if img.Status() != drivercore.ImageStatusDownloaded {
		t.Fatal("setup: the image should be Downloaded")
	}

	if err := img.Fetch(); err != nil {
		t.Fatal(err)
	}

	if srv.requests("/"+testVersion) != 1 {
		t.Errorf("want one download, got %d", srv.requests("/"+testVersion))
	}
	if got := readFile(t, e.cached(testVersion)); got != "content" {
		t.Errorf("cached file holds %q", got)
	}
}

// Another process fetches while this one waits for the lock: this one does
// nothing more.
func TestFetchSkipsAnImageThatBecomesDownloadedWhileWaiting(t *testing.T) {
	e, srv := fetchEnv(t, "content", nil)
	logs := captureLog(t)
	img := e.image(testVersion)

	unlock := holdLock(t, e.versionLock(testVersion))

	done := make(chan error, 1)
	go func() { done <- img.Fetch() }()

	select {
	case err := <-done:
		unlock()
		t.Fatalf("Fetch did not wait for the lock (returned %v)", err)
	case <-time.After(400 * time.Millisecond):
	}

	// The other process finishes: its file is cached, and the list says so.
	writeFile(t, e.cached(testVersion), "content")
	e.put(entry(testVersion, sum("content"), srv.url("/"+testVersion), "Downloaded", false))
	unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Fetch did not finish")
	}

	if srv.totalRequests() != 0 {
		t.Errorf("nothing should be downloaded, got %d requests", srv.totalRequests())
	}
	if img.Status() != drivercore.ImageStatusDownloaded {
		t.Error("the image must now be Downloaded")
	}

	found := false
	for _, line := range logs.lines {
		if line == "<2> The image for Kubernetes 1.30.1 has been downloaded by another process." {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a message about the other process: %q", logs.lines)
	}
}

// Several processes ask for the same image at once. One downloads, and the
// others find it done.
func TestConcurrentFetchesOfOneImageDownloadItOnce(t *testing.T) {
	content := strings.Repeat("big image ", 20000)
	e, srv := fetchEnv(t, content, nil)
	srv.delay = 150 * time.Millisecond
	logs := captureLog(t)

	const n = 6

	// Each "process" has its own Store and its own Image, got before any fetch.
	images := make([]*Image, n)
	for i := range images {
		store := e.anotherStore()
		img, err := store.GetImage(testVersion)
		if err != nil {
			t.Fatal(err)
		}
		images[i] = img.(*Image)
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})

	for i := range images {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = images[i].Fetch()
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("fetch %d: %v", i, err)
		}
		if images[i].Status() != drivercore.ImageStatusDownloaded {
			t.Errorf("image %d is %q", i, images[i].Status())
		}
	}

	if got := srv.requests("/" + testVersion); got != 1 {
		t.Errorf("the image was downloaded %d times, want 1", got)
	}
	if got := readFile(t, e.cached(testVersion)); got != content {
		t.Error("the cached file is wrong")
	}
	if e.statusOf(testVersion) != drivercore.ImageStatusDownloaded {
		t.Error("the list does not say Downloaded")
	}
	e.wantCacheOnly("kutti-" + testVersion + ".qcow2")

	skipped := 0
	for _, line := range logs.lines {
		if strings.Contains(line, "has been downloaded by another process") {
			skipped++
		}
	}
	if skipped != n-1 {
		t.Errorf("%d fetches skipped, want %d", skipped, n-1)
	}
}

func TestFetchFailsIfTheListHasChangedSinceTheImageWasGot(t *testing.T) {
	cases := map[string]func(e *imageEnv, srv *imageServer) string{
		"checksum changed": func(e *imageEnv, srv *imageServer) string {
			return list(entry(testVersion, "new checksum", srv.url("/"+testVersion), "Notdownloaded", false))
		},
		"URL changed": func(e *imageEnv, srv *imageServer) string {
			return list(entry(testVersion, sum("content"), srv.url("/elsewhere"), "Notdownloaded", false))
		},
		"entry removed": func(e *imageEnv, srv *imageServer) string { return `{}` },
	}

	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			e, srv := fetchEnv(t, "content", nil)
			img := e.image(testVersion)

			newlist := change(e, srv)
			writeFile(t, e.listpath, newlist)

			err := img.Fetch()
			if err == nil || !strings.Contains(err.Error(), "has changed or been removed in the image list") {
				t.Fatalf("got %v", err)
			}

			if srv.totalRequests() != 0 {
				t.Errorf("nothing may be downloaded, got %d requests", srv.totalRequests())
			}
			e.wantCacheOnly()
			if readFile(t, e.listpath) != newlist {
				t.Error("the list must not be changed")
			}
		})
	}
}

// The list is updated while the image downloads. The file is good for the
// image that was asked for, and is kept, but the list now has another image,
// so it must not be marked as downloaded.
func TestFetchWhenTheListChangesDuringTheDownload(t *testing.T) {
	e, srv := fetchEnv(t, "content", nil)
	img := e.image(testVersion)
	release := srv.hold()
	defer release()

	done := make(chan error, 1)
	go func() { done <- img.Fetch() }()

	select {
	case <-srv.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the download did not start")
	}

	newentry := entry(testVersion, "a newer checksum", srv.url("/"+testVersion), "Notdownloaded", false)
	e.put(newentry)
	release()

	err := <-done
	if err == nil || !strings.Contains(err.Error(), "changed while the image for Kubernetes 1.30.1 was being added") {
		t.Fatalf("got %v", err)
	}

	if e.statusOf(testVersion) != drivercore.ImageStatusNotDownloaded || img.Status() != drivercore.ImageStatusNotDownloaded {
		t.Error("the status must not change")
	}
	if readFile(t, e.listpath) != list(newentry) {
		t.Error("the new list must be as it was")
	}
	e.wantCacheOnly("kutti-" + testVersion + ".qcow2")
}

func TestFetchWithAnUnusableListFailsAndTouchesNothing(t *testing.T) {
	e, srv := fetchEnv(t, "content", nil)
	img := e.image(testVersion)
	writeFile(t, e.listpath, `{bad`)

	err := img.Fetch()
	var invalid *invalidListError
	if !errors.As(err, &invalid) {
		t.Fatalf("got %v", err)
	}
	if srv.totalRequests() != 0 {
		t.Error("nothing may be downloaded")
	}
	if len(corruptFiles(t, e.listpath)) != 0 || readFile(t, e.listpath) != `{bad` {
		t.Error("the list must be left alone")
	}
}

func TestFetchWhenTheCacheDirCannotBeMade(t *testing.T) {
	boom := errors.New("no cache")
	e, srv := fetchEnv(t, "content", func(c *Config) {
		c.CacheDir = func() (string, error) { return "", boom }
	})

	if err := e.image(testVersion).Fetch(); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if srv.totalRequests() != 0 {
		t.Error("nothing may be downloaded")
	}
}

func TestFetchRemovesOnlyStaleWorkDirsOfItsOwnVersion(t *testing.T) {
	e, _ := fetchEnv(t, "content", nil)

	old := time.Now().Add(-48 * time.Hour)
	mkdir := func(name string, mtime time.Time) {
		t.Helper()

		path := filepath.Join(e.cachedir, name)
		if err := os.MkdirAll(filepath.Join(path, "inside"), 0o777); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(path, "inside", "partial"), "partial download")
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}

	mkdir("kutti-k8s-1.30.1.111.work", old)                           // stale, this version: removed
	mkdir("kutti-k8s-1.30.1.222.work", time.Now())                    // recent, may be in use by another user: kept
	mkdir("kutti-k8s-1.30.1.666.work", time.Now().Add(-23*time.Hour)) // nearly a day old: kept
	mkdir("kutti-k8s-1.30.2.333.work", old)                           // stale, another version: kept
	mkdir("kutti-k8s-1.30.1.abc.work", old)                           // not made by this package: kept
	mkdir("kutti-k8s-1.30.1.444.work.bak", old)                       // not made by this package: kept
	oldfile := filepath.Join(e.cachedir, "kutti-k8s-1.30.1.555.work")
	writeFile(t, oldfile, "a file, not a directory")
	if err := os.Chtimes(oldfile, old, old); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(e.cachedir, "kutti-1.30.1.qcow2.old")
	writeFile(t, other, "unrelated")

	if err := e.image(testVersion).Fetch(); err != nil {
		t.Fatal(err)
	}

	e.wantCacheOnly(
		"kutti-1.30.1.qcow2",
		"kutti-1.30.1.qcow2.old",
		"kutti-k8s-1.30.1.222.work",
		"kutti-k8s-1.30.1.444.work.bak",
		"kutti-k8s-1.30.1.555.work",
		"kutti-k8s-1.30.1.666.work",
		"kutti-k8s-1.30.1.abc.work",
		"kutti-k8s-1.30.2.333.work",
	)
}

func TestIsWorkDirOf(t *testing.T) {
	cases := []struct {
		name, version string
		want          bool
	}{
		{"kutti-k8s-1.3.77.work", "1.3", true},
		{"kutti-k8s-1.30.1.4294967295.work", "1.30.1", true},
		{"kutti-k8s-1.3.1.77.work", "1.3", false}, // the work directory of 1.3.1
		{"kutti-k8s-1.3.77.work", "1.30", false},
		{"kutti-k8s-1.3..work", "1.3", false},
		{"kutti-k8s-1.3.work", "1.3", false},
		{"kutti-k8s-1.3.7a.work", "1.3", false},
		{"kutti-k8s-1.3.77.work.bak", "1.3", false},
		{"kutti-k8s-1.3.77abcde", "1.3", false}, // ends in five characters, but not ".work"
		{"xkutti-k8s-1.3.77.work", "1.3", false},
		{"kutti-1.3.77.work", "1.3", false},
		{"", "1.3", false},
	}

	for _, tc := range cases {
		if got := isWorkDirOf(tc.name, tc.version); got != tc.want {
			t.Errorf("isWorkDirOf(%q, %q) = %t, want %t", tc.name, tc.version, got, tc.want)
		}
	}
}

// A fetch makes a new work directory each time, and removes it.
func TestWorkDirsAreUniqueAndRemoved(t *testing.T) {
	var dirs []string
	e, _ := fetchEnv(t, "content", func(c *Config) {
		c.Unpack = func(path, workdir string) (string, func(), error) {
			dirs = append(dirs, workdir)
			return path, nil, nil
		}
	})

	for i := 0; i < 3; i++ {
		if err := e.image(testVersion).Fetch(); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]bool{}
	for _, d := range dirs {
		if seen[d] {
			t.Errorf("work directory %s was used twice", d)
		}
		seen[d] = true

		if _, err := os.Stat(d); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("work directory %s was not removed", d)
		}
	}
	if len(dirs) != 3 {
		t.Errorf("Unpack was called %d times, want 3", len(dirs))
	}
}

// A failure to copy the image into the cache is reported, and nothing is
// marked as downloaded.
func TestImportReportsAFailureToCopy(t *testing.T) {
	e := newImageEnv(t, nil)
	e.put(entry(testVersion, sum("x"), "u", "Notdownloaded", false))
	img := e.image(testVersion)

	// A directory with something in it is in the way of the file.
	if err := os.MkdirAll(filepath.Join(e.cached(testVersion), "x"), 0o777); err != nil {
		t.Fatal(err)
	}

	if err := img.FromFile(e.source("x")); err == nil {
		t.Fatal("expected an error")
	}
	if img.Status() != drivercore.ImageStatusNotDownloaded || e.statusOf(testVersion) != drivercore.ImageStatusNotDownloaded {
		t.Error("the status must not change")
	}
}

// The list becomes unusable while the image is imported. That is reported,
// and is not mistaken for the list having changed.
func TestImportReportsTheListBecomingUnusable(t *testing.T) {
	var e *imageEnv
	e = newImageEnv(t, func(c *Config) {
		c.Unpack = func(path, workdir string) (string, func(), error) {
			writeFile(t, e.listpath, `{bad`)
			return path, nil, nil
		}
	})
	e.put(entry(testVersion, sum("x"), "u", "Notdownloaded", false))
	img := e.image(testVersion)

	err := img.FromFile(e.source("x"))
	var invalid *invalidListError
	if !errors.As(err, &invalid) {
		t.Fatalf("got %v", err)
	}
	if img.Status() != drivercore.ImageStatusNotDownloaded {
		t.Error("the status must not change")
	}
	if readFile(t, e.listpath) != `{bad` || len(corruptFiles(t, e.listpath)) != 0 {
		t.Error("the unusable list must be left alone")
	}
}
