package imagecore

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kuttiproject/drivercore"
)

// The three drivers differ only in their Config. These tests set a Config the
// way each driver will, and check what each driver's users depend on: the
// names of the files, which are recorded in machines created by older
// versions, and the behaviour that comes from the hooks. They are the
// contract that a driver's migration to this package has to meet.

// profile describes how one driver configures imagecore.
type profile struct {
	name     string // also the name of the file of published list snapshot
	driver   string
	ext      string // image extension
	suffix   string // download suffix
	guarded  bool   // has an InUse function: qemu and hyperv
	unpacks  bool   // has an Unpack function: hyperv
	filename string // a file name that the driver accepts in FromFile
}

var profiles = []profile{
	{name: "qemu", driver: "driver-qemu", ext: ".qcow2", suffix: ".qcow2download", guarded: true, filename: "disk.img"},
	{name: "vbox", driver: "driver-vbox", ext: ".ova", suffix: ".ovadownload", filename: "disk.ova"},
	{name: "hyperv", driver: "driver-hyperv", ext: ".vhdx", suffix: ".download.zip", guarded: true, unpacks: true, filename: "disk.vhdx"},
}

// onlyVHDXOrZip is a stand-in for the hyperv driver's Unpack: a .vhdx is used
// as it is, a .zip is unzipped, and anything else is refused.
func onlyVHDXOrZip(path string, workdir string) (string, func(), error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".vhdx":
		return path, nil, nil
	case ".zip":
		return zipUnpack(path, workdir)
	}

	return "", nil, errors.New("only .vhdx or .zip files allowed")
}

// config returns a function that gives a Config the driver's settings, keeping
// the test's cache directory.
func (p profile) config(inuse func(string) (bool, error)) func(*Config) {
	return func(c *Config) {
		cachedir := c.CacheDir

		*c = Config{
			DriverName:     p.driver,
			ConfigFile:     p.driver + "-images.json",
			ImageExtension: p.ext,
			DownloadSuffix: p.suffix,
			SourceURL:      func() string { return "http://localhost/unused" },
			CacheDir:       cachedir,
		}
		if p.guarded {
			c.InUse = inuse
		}
		if p.unpacks {
			c.Unpack = onlyVHDXOrZip
		}
	}
}

// served returns what the image server serves for content: hyperv images come
// as a zip with one file in it.
func (p profile) served(t *testing.T, content string) string {
	t.Helper()

	if p.unpacks {
		return makeZip(t, "kutti-1.35"+p.ext, content)
	}

	return content
}

func (p profile) lockNames(version string) (list string, ver string) {
	return p.driver + "-images.lock", p.driver + "-images-" + version + ".lock"
}

func (p profile) sourceFile(e *imageEnv, content string) string {
	e.t.Helper()

	path := filepath.Join(e.t.TempDir(), p.filename)
	writeFile(e.t, path, content)

	return path
}

func TestProfilesNamesAndPublishedLists(t *testing.T) {
	for _, p := range profiles {
		t.Run(p.name, func(t *testing.T) {
			e := newImageEnv(t, p.config(nil))
			server := newListServer(t, testdata(t, "published-"+p.name+".json"))
			e.store.config.SourceURL = func() string { return server.URL }

			if err := e.store.UpdateImageList(); err != nil {
				t.Fatal(err)
			}

			if got := e.store.K8sVersions(); !reflect.DeepEqual(got, []string{"1.35", "1.36", "1.37"}) {
				t.Errorf("versions: %v", got)
			}

			// The list, and its lock, are in the config directory.
			listlock, _ := p.lockNames("1.35")
			want := []string{p.driver + "-images.json", listlock}
			if got := dirNames(t, e.configdir); !reflect.DeepEqual(got, want) {
				t.Errorf("config directory has %v, want %v", got, want)
			}

			// Differencing disks of hyperv and qemu refer to this path.
			path, err := e.store.ImagePath("1.35")
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(e.cachedir, "kutti-1.35"+p.ext); path != want {
				t.Errorf("image path %q, want %q", path, want)
			}
		})
	}
}

func TestProfilesFetchPurgeAndLockNames(t *testing.T) {
	for _, p := range profiles {
		t.Run(p.name, func(t *testing.T) {
			inuse := &inUseHook{}
			e := newImageEnv(t, p.config(inuse.fn))
			content := "disk image of " + p.name

			srv := newImageServer(t)
			srv.put("/1.35", p.served(t, content))
			e.put(entry("1.35", sum(content), srv.url("/1.35"), "Notdownloaded", false))
			img := e.image("1.35")

			if err := img.Fetch(); err != nil {
				t.Fatal(err)
			}

			cached := filepath.Join(e.cachedir, "kutti-1.35"+p.ext)
			if got := readFile(t, cached); got != content {
				t.Errorf("cached file holds %q", got)
			}
			e.wantCacheOnly("kutti-1.35" + p.ext)

			listlock, verlock := p.lockNames("1.35")
			want := []string{verlock, p.driver + "-images.json", listlock}
			if got := dirNames(t, e.configdir); !reflect.DeepEqual(got, want) {
				t.Errorf("config directory has %v, want %v", got, want)
			}

			if err := img.PurgeLocal(); err != nil {
				t.Fatal(err)
			}
			e.wantCacheOnly()
			if e.statusOf("1.35") != drivercore.ImageStatusNotDownloaded {
				t.Error("the image should be Notdownloaded")
			}
		})
	}
}

func TestProfilesAcceptedFiles(t *testing.T) {
	for _, p := range profiles {
		t.Run(p.name, func(t *testing.T) {
			e := newImageEnv(t, p.config(nil))
			content := "content"
			e.put(entry("1.35", sum(content), "u", "Notdownloaded", false))

			// The file the driver accepts.
			if err := e.image("1.35").FromFile(p.sourceFile(e, content)); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, filepath.Join(e.cachedir, "kutti-1.35"+p.ext)); got != content {
				t.Errorf("cached file holds %q", got)
			}

			// Anything else: hyperv refuses it, with the message of its own
			// Unpack. Vbox and qemu accept any file with the right checksum.
			other := filepath.Join(t.TempDir(), "disk.bin")
			writeFile(t, other, content)

			err := e.image("1.35").FromFile(other)
			if p.unpacks {
				if err == nil || err.Error() != "only .vhdx or .zip files allowed" {
					t.Errorf("got %v", err)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestProfilesHyperVAcceptsAZip(t *testing.T) {
	var p profile
	for _, candidate := range profiles {
		if candidate.unpacks {
			p = candidate
		}
	}

	e := newImageEnv(t, p.config(nil))
	e.put(entry("1.35", sum("inner"), "u", "Notdownloaded", false))

	archive := filepath.Join(t.TempDir(), "disk.zip")
	writeFile(t, archive, makeZip(t, "kutti-1.35.vhdx", "inner"))

	if err := e.image("1.35").FromFile(archive); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(e.cachedir, "kutti-1.35.vhdx")); got != "inner" {
		t.Errorf("cached file holds %q", got)
	}
}

// Qemu and hyperv have an in-use guard, and skip a copy that is not needed.
// Vbox has neither, because it copies the image when a machine is created.
func TestProfilesInUseGuard(t *testing.T) {
	for _, p := range profiles {
		t.Run(p.name, func(t *testing.T) {
			inuse := &inUseHook{inuse: true}
			e := newImageEnv(t, p.config(inuse.fn))
			cached := filepath.Join(e.cachedir, "kutti-1.35"+p.ext)

			e.put(entry("1.35", sum("new"), "u", "Notdownloaded", false))
			writeFile(t, cached, "old content")

			err := e.image("1.35").FromFile(p.sourceFile(e, "new"))

			if p.guarded {
				if err == nil || !strings.Contains(err.Error(), "it is in use as a backing disk") {
					t.Fatalf("got %v", err)
				}
				if got := readFile(t, cached); got != "old content" {
					t.Errorf("the image in use was replaced: %q", got)
				}
				if len(inuse.called()) != 1 {
					t.Errorf("InUse calls: %v", inuse.called())
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if got := readFile(t, cached); got != "new" {
					t.Errorf("cached file holds %q", got)
				}
			}
		})
	}
}

func TestProfilesPurgeOfAnImageInUse(t *testing.T) {
	for _, p := range profiles {
		t.Run(p.name, func(t *testing.T) {
			inuse := &inUseHook{inuse: true}
			e := newImageEnv(t, p.config(inuse.fn))
			cached := filepath.Join(e.cachedir, "kutti-1.35"+p.ext)

			e.put(entry("1.35", sum("x"), "u", "Downloaded", false))
			writeFile(t, cached, "x")

			err := e.image("1.35").PurgeLocal()

			if p.guarded {
				if err == nil || !strings.Contains(err.Error(), "it is in use as a backing disk") {
					t.Fatalf("got %v", err)
				}
				if _, statErr := os.Stat(cached); statErr != nil {
					t.Error("the image in use was removed")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				e.wantCacheOnly()
			}
		})
	}
}

func TestProfilesShortCircuit(t *testing.T) {
	for _, p := range profiles {
		t.Run(p.name, func(t *testing.T) {
			inuse := &inUseHook{inuse: true}
			e := newImageEnv(t, p.config(inuse.fn))
			cached := filepath.Join(e.cachedir, "kutti-1.35"+p.ext)

			// Importing the image that is already cached.
			e.put(entry("1.35", sum("same"), "u", "Notdownloaded", false))
			writeFile(t, cached, "same")
			setOld(t, cached)

			if err := e.image("1.35").FromFile(p.sourceFile(e, "same")); err != nil {
				t.Fatal(err)
			}

			if untouched := isOld(t, cached); untouched != p.guarded {
				t.Errorf("cached file untouched = %t, want %t", untouched, p.guarded)
			}
			if e.statusOf("1.35") != drivercore.ImageStatusDownloaded {
				t.Error("the image must be marked Downloaded")
			}
		})
	}
}
