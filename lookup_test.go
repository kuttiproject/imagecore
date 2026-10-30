package imagecore

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/kuttiproject/drivercore"
)

func TestGetImage(t *testing.T) {
	e := newImageEnv(t, nil)
	e.put(
		entry("1.29.2", "c29", "u29", "Downloaded", true),
		entry("1.30.1", "c30", "u30", "Notdownloaded", false),
	)

	img, err := e.store.GetImage("1.29.2")
	if err != nil {
		t.Fatal(err)
	}

	got, ok := img.(*Image)
	if !ok {
		t.Fatalf("got a %T", img)
	}
	if got.K8sVersion() != "1.29.2" || got.Status() != drivercore.ImageStatusDownloaded ||
		!got.Deprecated() || got.checksum != "c29" || got.sourceURL != "u29" || got.store != e.store {
		t.Errorf("wrong image: %+v", got)
	}
}

// The drivers returned a typed nil inside the interface for a version that is
// not there, which is not equal to nil. It must be a real nil.
func TestGetImageFailuresReturnARealNil(t *testing.T) {
	e := newImageEnv(t, nil)
	e.put(entry("1.30.1", "c", "u", "Notdownloaded", false))

	img, err := e.store.GetImage("9.9.9")
	if img != nil {
		t.Errorf("got %#v, want a nil interface", img)
	}
	if err == nil || err.Error() != "no image present for K8s version 9.9.9" {
		t.Errorf("got error %v", err)
	}

	// Not valid versions are simply not there.
	for _, v := range []string{"", "../x", "1.30"} {
		if img, err := e.store.GetImage(v); img != nil || err == nil {
			t.Errorf("GetImage(%q) = %v, %v", v, img, err)
		}
	}

	// An unusable list.
	writeFile(t, e.listpath, `{bad`)
	img, err = e.store.GetImage("1.30.1")
	var invalid *invalidListError
	if img != nil || !errors.As(err, &invalid) {
		t.Errorf("unusable list: got %v, %v", img, err)
	}

	// An invalid Config.
	img, err = New(Config{}).GetImage("1.30.1")
	if img != nil || err == nil || !strings.Contains(err.Error(), "invalid Config") {
		t.Errorf("invalid Config: got %v, %v", img, err)
	}
}

func TestListImages(t *testing.T) {
	e := newImageEnv(t, nil)
	e.put(
		entry("1.10.0", "c3", "u3", "Notdownloaded", false),
		entry("1.9.0", "c2", "u2", "Downloaded", false),
		entry("1.100.0", "c4", "u4", "Notdownloaded", false),
		entry("1.2.0", "c1", "u1", "Notdownloaded", true),
	)

	images, err := e.store.ListImages()
	if err != nil {
		t.Fatal(err)
	}

	var versions []string
	for _, img := range images {
		versions = append(versions, img.K8sVersion())

		if concrete, ok := img.(*Image); !ok || concrete.store != e.store {
			t.Errorf("image %s: got %T", img.K8sVersion(), img)
		}
	}

	if want := []string{"1.2.0", "1.9.0", "1.10.0", "1.100.0"}; !reflect.DeepEqual(versions, want) {
		t.Errorf("versions: got %v, want %v", versions, want)
	}
}

func TestListImagesOfAMissingListIsEmpty(t *testing.T) {
	e := newImageEnv(t, nil)

	images, err := e.store.ListImages()
	if err != nil || images == nil || len(images) != 0 {
		t.Errorf("got %#v, %v", images, err)
	}
}

func TestListImagesFailureGivesAnEmptySliceAndAnError(t *testing.T) {
	e := newImageEnv(t, nil)
	writeFile(t, e.listpath, `{bad`)

	images, err := e.store.ListImages()
	if err == nil || images == nil || len(images) != 0 {
		t.Errorf("got %#v, %v", images, err)
	}

	images, err = New(Config{}).ListImages()
	if err == nil || images == nil || len(images) != 0 {
		t.Errorf("invalid Config: got %#v, %v", images, err)
	}
}

func TestImagePath(t *testing.T) {
	e := newImageEnv(t, func(c *Config) { c.ImageExtension = ".vhdx" })

	got, err := e.store.ImagePath("1.30.1")
	if err != nil {
		t.Fatal(err)
	}
	if want := e.cachedir + "/kutti-1.30.1.vhdx"; filepathToSlash(got) != filepathToSlash(want) {
		t.Errorf("got %q, want %q", got, want)
	}

	// Nothing is created.
	if names := dirNames(t, e.cachedir); len(names) != 0 {
		t.Errorf("ImagePath must not create files, found %v", names)
	}
}

func TestImagePathRefusesInvalidVersions(t *testing.T) {
	e := newImageEnv(t, nil)

	for _, v := range []string{"", "../x", "a/b", `a\b`, ".1", "1 2", strings.Repeat("1", 65)} {
		if path, err := e.store.ImagePath(v); err == nil {
			t.Errorf("ImagePath(%q) = %q, want an error", v, path)
		}
	}
}

func TestImagePathErrors(t *testing.T) {
	if _, err := New(Config{}).ImagePath("1.30.1"); err == nil || !strings.Contains(err.Error(), "invalid Config") {
		t.Errorf("invalid Config: got %v", err)
	}

	boom := errors.New("no cache for you")
	e := newImageEnv(t, func(c *Config) { c.CacheDir = func() (string, error) { return "", boom } })
	if _, err := e.store.ImagePath("1.30.1"); !errors.Is(err, boom) {
		t.Errorf("CacheDir error: got %v", err)
	}
}

// Images that did not come from a Store cannot be fetched, imported or purged.
func TestImagesWithoutAStoreFailCleanly(t *testing.T) {
	var img Image
	if err := json.Unmarshal([]byte(entry("1.30.1", "c", "u", "Notdownloaded", false)[len(`"1.30.1":`):]), &img); err != nil {
		t.Fatal(err)
	}

	for name, call := range map[string]func() error{
		"Fetch":      img.Fetch,
		"FromFile":   func() error { return img.FromFile("x") },
		"PurgeLocal": img.PurgeLocal,
		"FetchWithProgress": func() error {
			return img.FetchWithProgress(func(int64, int64) {})
		},
	} {
		err := call()
		if err == nil || !strings.Contains(err.Error(), "does not belong to a Store") {
			t.Errorf("%s: got %v", name, err)
		}
	}
}
