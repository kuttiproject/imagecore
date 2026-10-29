package imagecore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kuttiproject/drivercore"
	"github.com/kuttiproject/workspace"
)

// image builds one entry of a list file, in the format the drivers write.
func entry(version, checksum, url, status string, deprecated bool) string {
	return fmt.Sprintf(
		`%q:{"ImageK8sVersion":%q,"ImageChecksum":%q,"ImageSourceURL":%q,"ImageStatus":%q,"ImageDeprecated":%t}`,
		version, version, checksum, url, status, deprecated,
	)
}

func list(entries ...string) string {
	return "{" + strings.Join(entries, ",") + "}"
}

func TestUpdateWithNoLocalListSavesTheDownloadedOne(t *testing.T) {
	dir := testWorkspace(t)
	server := newListServer(t, list(
		entry("1.35", "c35", "u35", "Notdownloaded", false),
		entry("1.36", "c36", "u36", "Notdownloaded", true),
	))
	url := server.URL
	store, path := newTestStore(t, &url)

	if err := store.UpdateImageList(); err != nil {
		t.Fatal(err)
	}

	// Compact, keys in order, fields in the old order: the bytes the drivers
	// wrote for the same list.
	want := list(
		entry("1.35", "c35", "u35", "Notdownloaded", false),
		entry("1.36", "c36", "u36", "Notdownloaded", true),
	)
	if got := readFile(t, path); got != want {
		t.Errorf("saved list differs.\nwant: %s\ngot:  %s", want, got)
	}

	if got := store.K8sVersions(); !reflect.DeepEqual(got, []string{"1.35", "1.36"}) {
		t.Errorf("K8sVersions: got %v", got)
	}

	// Only the list and its lock are left: no temporary files.
	if got, want := dirNames(t, dir), []string{"driver-qemu-images.json", "driver-qemu-images.lock"}; !reflect.DeepEqual(got, want) {
		t.Errorf("files in the config directory: got %v, want %v", got, want)
	}
}

// The lists as published on GitHub in October 2026, one per driver. They differ
// in the order of fields, and the hyperv one was published with Windows line
// ends. The snapshots are kept with Unix line ends, which patches and
// checkouts leave alone, and each is served with both.
func TestUpdateWithThePublishedLists(t *testing.T) {
	endings := map[string]string{"LF": "\n", "CRLF": "\r\n"}

	for _, driver := range []string{"qemu", "vbox", "hyperv"} {
		for ending, eol := range endings {
			t.Run(driver+"/"+ending, func(t *testing.T) {
				testWorkspace(t)

				body := strings.ReplaceAll(testdata(t, "published-"+driver+".json"), "\r\n", "\n")
				body = strings.ReplaceAll(body, "\n", eol)
				if got := strings.Contains(body, "\r\n"); got != (ending == "CRLF") {
					t.Fatalf("test list has the wrong line ends: CRLF present = %t", got)
				}

				server := newListServer(t, body)
				url := server.URL
				store, _ := newTestStore(t, &url)

				if err := store.UpdateImageList(); err != nil {
					t.Fatal(err)
				}

				images, err := store.images()
				if err != nil {
					t.Fatal(err)
				}

				var versions []string
				for _, img := range images {
					versions = append(versions, img.k8sVersion)

					if img.checksum == "" || !strings.Contains(img.sourceURL, "driver-"+driver+"-images") {
						t.Errorf("%s: checksum or URL missing: %+v", img.k8sVersion, img)
					}
					if img.deprecated {
						t.Errorf("%s: should not be deprecated", img.k8sVersion)
					}
					// The published lists spell the status "NotDownloaded", which
					// is not the constant. It must not get through.
					if img.status != drivercore.ImageStatusNotDownloaded {
						t.Errorf("%s: status is %q", img.k8sVersion, img.status)
					}
				}

				if want := []string{"1.35", "1.36", "1.37"}; !reflect.DeepEqual(versions, want) {
					t.Errorf("versions: got %v, want %v", versions, want)
				}
			})
		}
	}
}

func TestUpdateKeepsDownloadedOnlyForUnchangedImages(t *testing.T) {
	testWorkspace(t)

	// The local list has these, all but 1.31 downloaded.
	local := list(
		entry("1.28", "a", "ua", "Downloaded", true),
		entry("1.29", "b", "ub", "Downloaded", false),
		entry("1.30", "c", "uc", "Downloaded", false),
		entry("1.31", "d", "ud", "Notdownloaded", false),
		entry("1.32", "e", "ue", "Unknown", false),
		entry("1.99", "z", "uz", "Downloaded", false),
	)

	// The published list: 1.28 same; 1.29 new checksum; 1.30 new URL; 1.31
	// same but was never downloaded; 1.32 same but Unknown; 1.33 is new, and
	// claims to be downloaded; 1.99 is gone. The published statuses are
	// ignored, and the Deprecated flag comes from the published list.
	published := list(
		entry("1.28", "a", "ua", "NotDownloaded", false),
		entry("1.29", "b2", "ub", "Notdownloaded", false),
		entry("1.30", "c", "uc2", "Notdownloaded", false),
		entry("1.31", "d", "ud", "Downloaded", false),
		entry("1.32", "e", "ue", "Downloaded", false),
		entry("1.33", "f", "uf", "Downloaded", false),
	)

	server := newListServer(t, published)
	url := server.URL
	store, path := newTestStore(t, &url)
	writeFile(t, path, local)

	if err := store.UpdateImageList(); err != nil {
		t.Fatal(err)
	}

	want := list(
		entry("1.28", "a", "ua", "Downloaded", false),
		entry("1.29", "b2", "ub", "Notdownloaded", false),
		entry("1.30", "c", "uc2", "Notdownloaded", false),
		entry("1.31", "d", "ud", "Notdownloaded", false),
		entry("1.32", "e", "ue", "Notdownloaded", false),
		entry("1.33", "f", "uf", "Notdownloaded", false),
	)
	if got := readFile(t, path); got != want {
		t.Errorf("saved list differs.\nwant: %s\ngot:  %s", want, got)
	}

	if store.ValidK8sVersion("1.99") {
		t.Error("an image that is not in the published list is dropped")
	}
}

func TestUpdateNeedsAllOfChecksumAndURLToMatch(t *testing.T) {
	// Each row changes one thing about an image that was downloaded.
	cases := []struct {
		name         string
		checksum     string
		url          string
		wantDownload bool
	}{
		{"nothing changed", "c", "u", true},
		{"checksum changed", "c2", "u", false},
		{"URL changed", "c", "u2", false},
		{"both changed", "c2", "u2", false},
		{"checksum emptied", "", "u", false},
		{"URL emptied", "c", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testWorkspace(t)
			server := newListServer(t, list(entry("1.30", tc.checksum, tc.url, "Notdownloaded", false)))
			url := server.URL
			store, path := newTestStore(t, &url)
			writeFile(t, path, list(entry("1.30", "c", "u", "Downloaded", false)))

			if err := store.UpdateImageList(); err != nil {
				t.Fatal(err)
			}

			images, err := store.images()
			if err != nil || len(images) != 1 {
				t.Fatalf("got %v, %v", images, err)
			}

			downloaded := images[0].status == drivercore.ImageStatusDownloaded
			if downloaded != tc.wantDownload {
				t.Errorf("downloaded = %t, want %t", downloaded, tc.wantDownload)
			}
		})
	}
}

func TestUpdateWithAJSONNullListSavesAnEmptyList(t *testing.T) {
	testWorkspace(t)
	server := newListServer(t, `null`)
	url := server.URL
	store, path := newTestStore(t, &url)

	if err := store.UpdateImageList(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != `{}` {
		t.Errorf("got %s", got)
	}
}

func TestUpdateAcceptsAnEmptyList(t *testing.T) {
	testWorkspace(t)
	server := newListServer(t, `{}`)
	url := server.URL
	store, path := newTestStore(t, &url)
	writeFile(t, path, list(entry("1.30", "c", "u", "Downloaded", false)))

	if err := store.UpdateImageList(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != `{}` {
		t.Errorf("got %s", got)
	}
}

func TestUpdateRejectsAnInvalidDownloadedList(t *testing.T) {
	good := entry("1.35", "c", "u", "Notdownloaded", false)

	cases := []struct {
		name string
		body string
		want string // text the error must contain, besides the URL
	}{
		{"not JSON", `<html>not found</html>`, "could not be parsed"},
		{"empty", ``, "could not be parsed"},
		{"an array", `[]`, "could not be parsed"},
		{"a string entry", `{"1.30":"x"}`, "could not be parsed"},
		{"bad version key", list(good, `"../x":{"ImageK8sVersion":"../x"}`), `"../x"`},
		{"empty version key", list(good, `"":{"ImageK8sVersion":""}`), `version ""`},
		{"too long a version", list(good, fmt.Sprintf(`%q:{"ImageK8sVersion":%q}`, strings.Repeat("1", 65), strings.Repeat("1", 65))), "not valid"},
		{"null entry", list(good, `"1.36":null`), `"1.36"`},
		{"entry for another version", list(good, `"1.36":{"ImageK8sVersion":"1.37"}`), `"1.37"`},
		{"entry without a version", list(good, `"1.36":{}`), `"1.36"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := testWorkspace(t)
			logs := captureLog(t)
			server := newListServer(t, tc.body)
			url := server.URL
			store, path := newTestStore(t, &url)

			local := list(entry("1.30", "c", "u", "Downloaded", false))
			writeFile(t, path, local)

			err := store.UpdateImageList()
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, want := range []string{server.URL, tc.want} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}

			if got := readFile(t, path); got != local {
				t.Errorf("the local list was changed to %s", got)
			}
			if got := dirNames(t, dir); !reflect.DeepEqual(got, []string{"driver-qemu-images.json"}) {
				t.Errorf("files in the config directory: %v", got)
			}

			logged := logs.errors()
			if len(logged) != 1 {
				t.Fatalf("want one error logged, got %q", logged)
			}
			for _, want := range []string{server.URL, tc.want, "whole list was rejected", "current list has been kept"} {
				if !strings.Contains(logged[0], want) {
					t.Errorf("logged error %q does not contain %q", logged[0], want)
				}
			}
		})
	}
}

func TestUpdateRejectsAListThatIsTooLarge(t *testing.T) {
	dir := testWorkspace(t)
	logs := captureLog(t)

	// Valid JSON, but far bigger than any real list.
	server := newListServer(t, `{}`+strings.Repeat(" ", maxListSize))
	url := server.URL
	store, path := newTestStore(t, &url)
	local := list(entry("1.30", "c", "u", "Downloaded", false))
	writeFile(t, path, local)

	err := store.UpdateImageList()
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("want a 'too large' error, got %v", err)
	}
	if readFile(t, path) != local {
		t.Error("the local list was changed")
	}
	if got := dirNames(t, dir); !reflect.DeepEqual(got, []string{"driver-qemu-images.json"}) {
		t.Errorf("files in the config directory: %v", got)
	}
	if len(logs.errors()) != 1 {
		t.Errorf("want one error logged, got %q", logs.errors())
	}

	// One byte less is fine.
	server.serve(`{}` + strings.Repeat(" ", maxListSize-2))
	if err := store.UpdateImageList(); err != nil {
		t.Errorf("a list at the limit should be accepted: %v", err)
	}
}

func TestUpdateFailedDownloadKeepsTheList(t *testing.T) {
	dir := testWorkspace(t)
	server := newListServer(t, list(entry("1.35", "c", "u", "Notdownloaded", false)))
	server.failWith(500)
	url := server.URL
	store, path := newTestStore(t, &url)
	local := list(entry("1.30", "c", "u", "Downloaded", false))
	writeFile(t, path, local)

	err := store.UpdateImageList()
	if err == nil {
		t.Fatal("expected an error")
	}
	// It is the failed download that is reported, not what was left behind.
	if strings.Contains(err.Error(), "parsed") {
		t.Errorf("the error should be about the download: %v", err)
	}

	if readFile(t, path) != local {
		t.Error("the local list was changed")
	}
	if got := dirNames(t, dir); !reflect.DeepEqual(got, []string{"driver-qemu-images.json"}) {
		t.Errorf("files in the config directory: %v", got)
	}

	// An unreachable server is the same.
	server.Close()
	err = store.UpdateImageList()
	if err == nil || strings.Contains(err.Error(), "parsed") {
		t.Fatalf("want a download error, got %v", err)
	}
	if readFile(t, path) != local {
		t.Error("the local list was changed")
	}
}

// The source URL is read when an update starts, not when the Store is made.
func TestUpdateReadsTheSourceURLWhenItRuns(t *testing.T) {
	testWorkspace(t)
	first := newListServer(t, list(entry("1.30", "c", "u", "Notdownloaded", false)))
	second := newListServer(t, list(entry("1.31", "c", "u", "Notdownloaded", false)))

	url := first.URL
	store, _ := newTestStore(t, &url)

	url = second.URL
	if err := store.UpdateImageList(); err != nil {
		t.Fatal(err)
	}

	if first.requests() != 0 || second.requests() != 1 {
		t.Errorf("requests: first %d, second %d", first.requests(), second.requests())
	}
	if got := store.K8sVersions(); !reflect.DeepEqual(got, []string{"1.31"}) {
		t.Errorf("K8sVersions: got %v", got)
	}
}

func TestUpdateReplacesAnUnusableLocalList(t *testing.T) {
	cases := map[string]string{
		"not JSON":      `{bad`,
		"wrong version": `{"1.30":{"ImageK8sVersion":"1.31"}}`,
		"null entry":    `{"1.30":null}`,
	}

	for name, damaged := range cases {
		t.Run(name, func(t *testing.T) {
			testWorkspace(t)
			logs := captureLog(t)
			server := newListServer(t, list(entry("1.35", "c", "u", "Downloaded", false)))
			url := server.URL
			store, path := newTestStore(t, &url)
			writeFile(t, path, damaged)

			if err := store.UpdateImageList(); err != nil {
				t.Fatal(err)
			}

			// The new list is in place, and nothing from the damaged one is.
			want := list(entry("1.35", "c", "u", "Notdownloaded", false))
			if got := readFile(t, path); got != want {
				t.Errorf("saved list differs.\nwant: %s\ngot:  %s", want, got)
			}

			// The damaged file is set aside, untouched.
			aside := corruptFiles(t, path)
			if len(aside) != 1 || readFile(t, aside[0]) != damaged {
				t.Fatalf("want one set-aside copy of the damaged file, got %v", aside)
			}

			logged := logs.errors()
			if len(logged) != 1 || !strings.Contains(logged[0], aside[0]) || !strings.Contains(logged[0], path) {
				t.Errorf("want one logged error naming both files, got %q", logged)
			}
		})
	}
}

// Setting aside twice in the same millisecond must not lose the first copy.
func TestSetAsideNeverReplacesAnExistingCopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "list.json")

	seen := map[string]string{}
	for i := 0; i < 5; i++ {
		content := fmt.Sprintf("damaged %d", i)
		writeFile(t, path, content)

		backup, err := setAside(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, dup := seen[backup]; dup {
			t.Fatalf("%s was used twice", backup)
		}
		seen[backup] = content
	}

	for backup, content := range seen {
		if got := readFile(t, backup); got != content {
			t.Errorf("%s holds %q, want %q", backup, got, content)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("the original must be gone")
	}
}

func TestSetAsideOfAMissingFileIsNotAnError(t *testing.T) {
	if _, err := setAside(filepath.Join(t.TempDir(), "nothing.json")); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// A list that cannot be read is not a damaged list. It must not be moved, and
// the update must fail.
func TestUpdateDoesNotReplaceAListItCannotRead(t *testing.T) {
	dir := testWorkspace(t)
	server := newListServer(t, list(entry("1.35", "c", "u", "Notdownloaded", false)))
	url := server.URL
	store, path := newTestStore(t, &url)

	if err := os.Mkdir(path, 0o777); err != nil {
		t.Fatal(err)
	}

	if err := store.UpdateImageList(); err == nil {
		t.Fatal("expected an error")
	}

	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		t.Errorf("the unreadable list must stay where it is: %v", err)
	}
	for _, name := range dirNames(t, dir) {
		if strings.HasSuffix(name, ".corrupt") || strings.HasSuffix(name, ".new") {
			t.Errorf("unexpected file %s", name)
		}
	}

	// The lock must have been released, even though the update failed.
	unlock, err := workspace.LockFile(filepath.Join(dir, "driver-qemu-images.lock"), 500*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("the list lock was not released: %v", err)
	}
	unlock()
}

func listLockPath(t *testing.T, dir string) string {
	t.Helper()
	return filepath.Join(dir, "driver-qemu-images.lock")
}

func TestUpdateWaitsForTheListLock(t *testing.T) {
	dir := testWorkspace(t)
	server := newListServer(t, list(entry("1.35", "c", "u", "Notdownloaded", false)))
	url := server.URL
	store, path := newTestStore(t, &url)

	unlock, err := workspace.LockFile(listLockPath(t, dir), time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- store.UpdateImageList() }()

	select {
	case err := <-done:
		unlock()
		t.Fatalf("UpdateImageList did not wait for the lock (returned %v)", err)
	case <-time.After(400 * time.Millisecond):
	}

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("the list must not be written while the lock is held")
	}

	unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("UpdateImageList did not finish after the lock was released")
	}

	if !store.ValidK8sVersion("1.35") {
		t.Error("the list was not written")
	}
}

func TestUpdateGivesUpWaitingForTheListLock(t *testing.T) {
	dir := testWorkspace(t)
	server := newListServer(t, list(entry("1.35", "c", "u", "Notdownloaded", false)))
	url := server.URL
	store, path := newTestStore(t, &url)

	old := listLockTimeout
	listLockTimeout = 200 * time.Millisecond
	t.Cleanup(func() { listLockTimeout = old })

	unlock, err := workspace.LockFile(listLockPath(t, dir), time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	err = store.UpdateImageList()
	if err == nil || !strings.Contains(err.Error(), "lock") {
		t.Fatalf("want a lock error, got %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("the list must not be written without the lock")
	}
}

// Reading takes no lock, so it works while an update holds it.
func TestReadsDoNotWaitForTheListLock(t *testing.T) {
	dir := testWorkspace(t)
	url := "http://localhost/unused"
	store, path := newTestStore(t, &url)
	writeFile(t, path, list(entry("1.30", "c", "u", "Notdownloaded", false)))

	unlock, err := workspace.LockFile(listLockPath(t, dir), time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	done := make(chan bool, 1)
	go func() { done <- store.ValidK8sVersion("1.30") }()

	select {
	case ok := <-done:
		if !ok {
			t.Error("1.30 should be valid")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a read waited for the list lock")
	}
}

// Updates and reads from several goroutines, which the race detector watches.
// Whatever the interleaving, every read sees a whole list, never a partial one.
func TestConcurrentUpdatesAndReads(t *testing.T) {
	dir := testWorkspace(t)
	server := newListServer(t, testdata(t, "published-qemu.json"))
	url := server.URL
	store, path := newTestStore(t, &url)

	var wg sync.WaitGroup
	errs := make(chan error, 64)

	for i := 0; i < 6; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				if err := store.UpdateImageList(); err != nil {
					errs <- err
				}
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				images, err := store.images()
				if err != nil {
					errs <- err
					continue
				}
				if n := len(images); n != 0 && n != 3 {
					errs <- fmt.Errorf("read a list of %d images", n)
				}
			}
		}()
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	if got := store.K8sVersions(); !reflect.DeepEqual(got, []string{"1.35", "1.36", "1.37"}) {
		t.Errorf("K8sVersions: got %v", got)
	}

	var saved map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readFile(t, path)), &saved); err != nil {
		t.Errorf("the saved list is not valid JSON: %v", err)
	}

	if got, want := dirNames(t, dir), []string{"driver-qemu-images.json", "driver-qemu-images.lock"}; !reflect.DeepEqual(got, want) {
		t.Errorf("files in the config directory: got %v, want %v", got, want)
	}
}
