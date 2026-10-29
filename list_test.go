package imagecore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kuttiproject/drivercore"
)

func TestReadsOfAMissingListAreEmptyAndWriteNothing(t *testing.T) {
	dir := testWorkspace(t)
	url := "http://localhost/unused"
	store, path := newTestStore(t, &url)

	if store.ValidK8sVersion("1.30.1") {
		t.Error("no list, so no valid version")
	}

	versions := store.K8sVersions()
	if versions == nil || len(versions) != 0 {
		t.Errorf("want an empty, non-nil slice, got %#v", versions)
	}

	images, err := store.images()
	if err != nil || len(images) != 0 {
		t.Errorf("want no images and no error, got %v, %v", images, err)
	}

	if names := dirNames(t, dir); len(names) != 0 {
		t.Errorf("reading must not create files, found %v", names)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the list file must not exist: %v", err)
	}
}

func TestReadsUseTheLocalList(t *testing.T) {
	testWorkspace(t)
	url := "http://localhost/unused"
	store, path := newTestStore(t, &url)
	writeFile(t, path, oldFormat)

	if !store.ValidK8sVersion("1.28.4") || !store.ValidK8sVersion("1.30.1") {
		t.Error("both versions in the list must be valid")
	}
	if store.ValidK8sVersion("9.9.9") || store.ValidK8sVersion("") || store.ValidK8sVersion("1.28") {
		t.Error("a version that is not in the list must not be valid")
	}

	if got, want := store.K8sVersions(), []string{"1.28.4", "1.30.1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("K8sVersions: got %v, want %v", got, want)
	}

	images, err := store.images()
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 2 {
		t.Fatalf("got %d images, want 2", len(images))
	}

	old, cur := images[0], images[1]
	if old.K8sVersion() != "1.28.4" || old.Status() != drivercore.ImageStatusDownloaded ||
		!old.Deprecated() || old.checksum != "abc123" ||
		old.sourceURL != "https://example.com/kutti-1.28.4.qcow2" {
		t.Errorf("1.28.4 wrong: %+v", old)
	}
	if cur.K8sVersion() != "1.30.1" || cur.Status() != drivercore.ImageStatusNotDownloaded || cur.Deprecated() {
		t.Errorf("1.30.1 wrong: %+v", cur)
	}

	for _, img := range images {
		if img.store != store {
			t.Errorf("image %s does not belong to the Store", img.k8sVersion)
		}
	}
}

// Every call reads the file again, so a change made by another process is seen
// by a Store that already exists.
func TestEveryReadReloadsTheFile(t *testing.T) {
	testWorkspace(t)
	url := "http://localhost/unused"
	store, path := newTestStore(t, &url)

	writeFile(t, path, `{"1.30":{"ImageK8sVersion":"1.30"}}`)
	if !store.ValidK8sVersion("1.30") || store.ValidK8sVersion("1.31") {
		t.Fatal("first list read wrongly")
	}

	writeFile(t, path, `{"1.31":{"ImageK8sVersion":"1.31"}}`)
	if store.ValidK8sVersion("1.30") || !store.ValidK8sVersion("1.31") {
		t.Error("a changed file must be seen by the next call")
	}
	if got := store.K8sVersions(); !reflect.DeepEqual(got, []string{"1.31"}) {
		t.Errorf("K8sVersions: got %v", got)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if store.ValidK8sVersion("1.31") || len(store.K8sVersions()) != 0 {
		t.Error("a removed file is an empty list")
	}
}

func TestReadsAreInVersionOrder(t *testing.T) {
	testWorkspace(t)
	url := "http://localhost/unused"
	store, path := newTestStore(t, &url)

	var entries []string
	for _, v := range []string{"1.100", "1.10", "1.9", "1.35", "1.2", "1.9.1", "2"} {
		entries = append(entries, fmt.Sprintf(`%q:{"ImageK8sVersion":%q}`, v, v))
	}
	writeFile(t, path, "{"+strings.Join(entries, ",")+"}")

	want := []string{"1.2", "1.9", "1.9.1", "1.10", "1.35", "1.100", "2"}

	// The order must not depend on map iteration order, so ask many times.
	for i := 0; i < 30; i++ {
		if got := store.K8sVersions(); !reflect.DeepEqual(got, want) {
			t.Fatalf("K8sVersions: got %v, want %v", got, want)
		}

		images, err := store.images()
		if err != nil {
			t.Fatal(err)
		}
		for j, img := range images {
			if img.k8sVersion != want[j] {
				t.Fatalf("images()[%d] is %s, want %s", j, img.k8sVersion, want[j])
			}
		}
	}
}

func TestReadsOfAnUnusableListFailAndTouchNothing(t *testing.T) {
	cases := map[string]string{
		"not JSON":        `{bad`,
		"empty file":      ``,
		"an array":        `[]`,
		"a string entry":  `{"1.30":"x"}`,
		"null entry":      `{"1.30":null}`,
		"wrong version":   `{"1.30":{"ImageK8sVersion":"1.31"}}`,
		"bad key":         `{"../x":{"ImageK8sVersion":"../x"}}`,
		"one bad of many": `{"1.30":{"ImageK8sVersion":"1.30"},"a/b":{"ImageK8sVersion":"a/b"}}`,
	}

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := testWorkspace(t)
			logs := captureLog(t)
			url := "http://localhost/unused"
			store, path := newTestStore(t, &url)
			writeFile(t, path, content)

			_, err := store.images()
			var invalid *invalidListError
			if !errors.As(err, &invalid) {
				t.Fatalf("want an *invalidListError, got %v", err)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error %q does not name the file", err)
			}

			if store.ValidK8sVersion("1.30") {
				t.Error("no version is valid when the list cannot be used")
			}
			if got := store.K8sVersions(); got == nil || len(got) != 0 {
				t.Errorf("want an empty, non-nil slice, got %#v", got)
			}

			// The two methods that cannot return an error log it instead.
			logged := logs.errors()
			if len(logged) != 2 {
				t.Fatalf("want 2 errors logged, got %q", logged)
			}
			for _, line := range logged {
				if !strings.Contains(line, path) {
					t.Errorf("logged error %q does not name the file", line)
				}
			}

			if readFile(t, path) != content {
				t.Error("the list file was changed")
			}
			if names := dirNames(t, dir); len(names) != 1 {
				t.Errorf("no other file may appear, found %v", names)
			}
		})
	}
}

func TestReadOfAnUnreadableListIsNotAnInvalidList(t *testing.T) {
	testWorkspace(t)
	url := "http://localhost/unused"
	store, path := newTestStore(t, &url)

	// A directory where the file should be cannot be read, whatever the user.
	if err := os.Mkdir(path, 0o777); err != nil {
		t.Fatal(err)
	}

	_, err := store.images()
	if err == nil {
		t.Fatal("expected an error")
	}

	var invalid *invalidListError
	if errors.As(err, &invalid) {
		t.Errorf("an I/O error is not an invalid list: %v", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the file", err)
	}
}

func TestAnInvalidConfigIsReported(t *testing.T) {
	dir := testWorkspace(t)
	store := New(Config{})

	if _, err := store.images(); err == nil || !strings.Contains(err.Error(), "invalid Config") {
		t.Errorf("images: got %v", err)
	}
	if err := store.UpdateImageList(); err == nil || !strings.Contains(err.Error(), "invalid Config") {
		t.Errorf("UpdateImageList: got %v", err)
	}
	if store.ValidK8sVersion("1.30") || len(store.K8sVersions()) != 0 {
		t.Error("an invalid Config gives no versions")
	}

	if names := dirNames(t, dir); len(names) != 0 {
		t.Errorf("nothing may be created, found %v", names)
	}
}

// The list file lives in the config directory, under the configured name.
func TestListPath(t *testing.T) {
	dir := testWorkspace(t)
	url := "http://localhost/unused"
	_, path := newTestStore(t, &url)

	if want := filepath.Join(dir, "driver-qemu-images.json"); path != want {
		t.Errorf("got %q, want %q", path, want)
	}
}

// A JSON null is an empty list, not an invalid one.
func TestAJSONNullLocalListIsEmpty(t *testing.T) {
	testWorkspace(t)
	url := "http://localhost/unused"
	store, path := newTestStore(t, &url)
	writeFile(t, path, `null`)

	images, err := store.images()
	if err != nil || len(images) != 0 {
		t.Errorf("want no images and no error, got %v, %v", images, err)
	}
	if got := store.K8sVersions(); got == nil || len(got) != 0 {
		t.Errorf("want an empty, non-nil slice, got %#v", got)
	}
}

// The reason a list could not be used stays available to the caller.
func TestListErrorsKeepTheirCause(t *testing.T) {
	testWorkspace(t)
	url := "http://localhost/unused"
	store, path := newTestStore(t, &url)
	writeFile(t, path, `{bad`)

	_, err := store.images()

	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) {
		t.Errorf("want the JSON syntax error in the chain, got %v", err)
	}
}
