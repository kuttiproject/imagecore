package imagecore

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/kuttiproject/drivercore"
	"github.com/kuttiproject/workspace"
)

const testVersion = "1.30.1"

func sum(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

// imageEnv is a Store set up in a temporary workspace, with a cache directory
// of its own, and helpers to look at the result.
type imageEnv struct {
	t         *testing.T
	store     *Store
	listpath  string
	configdir string
	cachedir  string
}

// newImageEnv makes an imageEnv. modify, if not nil, changes the Config
// before the Store is made.
func newImageEnv(t *testing.T, modify func(*Config)) *imageEnv {
	t.Helper()

	configdir := testWorkspace(t)
	cachedir := t.TempDir()

	config := validConfig()
	config.CacheDir = func() (string, error) { return cachedir, nil }
	if modify != nil {
		modify(&config)
	}

	store := New(config)
	path, err := store.listPath()
	if err != nil {
		t.Fatal(err)
	}

	return &imageEnv{t: t, store: store, listpath: path, configdir: configdir, cachedir: cachedir}
}

// put writes the local list.
func (e *imageEnv) put(entries ...string) {
	e.t.Helper()
	writeFile(e.t, e.listpath, list(entries...))
}

// image returns an image from the Store.
func (e *imageEnv) image(version string) *Image {
	e.t.Helper()

	img, err := e.store.GetImage(version)
	if err != nil {
		e.t.Fatal(err)
	}

	return img.(*Image)
}

// anotherStore returns a second Store with the same Config, which stands for
// another process of the same user.
func (e *imageEnv) anotherStore() *Store {
	return New(e.store.config)
}

// cached returns the path of the cached file for a version.
func (e *imageEnv) cached(version string) string {
	e.t.Helper()

	path, err := e.store.ImagePath(version)
	if err != nil {
		e.t.Fatal(err)
	}

	return path
}

// statusOf returns the status of a version in the local list file.
func (e *imageEnv) statusOf(version string) drivercore.ImageStatus {
	e.t.Helper()

	var l map[string]*Image
	if err := json.Unmarshal([]byte(readFile(e.t, e.listpath)), &l); err != nil {
		e.t.Fatal(err)
	}
	if l[version] == nil {
		e.t.Fatalf("no entry for %s in the list", version)
	}

	return l[version].status
}

// source writes content to a new file outside the cache, and returns its path.
func (e *imageEnv) source(content string) string {
	e.t.Helper()

	path := filepath.Join(e.t.TempDir(), "source-file")
	writeFile(e.t, path, content)

	return path
}

// versionLock returns the path of the lock for a version.
func (e *imageEnv) versionLock(version string) string {
	return filepath.Join(e.configdir, "driver-qemu-images-"+version+".lock")
}

func (e *imageEnv) wantCacheOnly(files ...string) {
	e.t.Helper()

	names := dirNames(e.t, e.cachedir)
	if len(names) != len(files) {
		e.t.Errorf("cache directory has %v, want %v", names, files)
		return
	}
	for i := range files {
		if names[i] != files[i] {
			e.t.Errorf("cache directory has %v, want %v", names, files)
			return
		}
	}
}

func makeZip(t *testing.T, name string, content string) string {
	t.Helper()

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.String()
}

// zipUnpack is an Unpack function like the hyperv driver's: it extracts the
// one file in a zip into workdir.
func zipUnpack(path string, workdir string) (string, func(), error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return "", nil, err
	}
	defer r.Close()

	if len(r.File) != 1 {
		return "", nil, fmt.Errorf("invalid compressed image")
	}

	rc, err := r.File[0].Open()
	if err != nil {
		return "", nil, err
	}
	defer rc.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(rc); err != nil {
		return "", nil, err
	}

	out := filepath.Join(workdir, "unpacked")
	if err := os.WriteFile(out, buf.Bytes(), 0o666); err != nil {
		return "", nil, err
	}

	return out, nil, nil
}

// imageServer serves image files, counts requests, and can hold requests
// until told to go on.
type imageServer struct {
	*httptest.Server

	mu      sync.Mutex
	files   map[string]string
	hits    map[string]int
	status  int
	delay   time.Duration
	gate    chan struct{}
	started chan struct{}
}

func newImageServer(t *testing.T) *imageServer {
	t.Helper()

	s := &imageServer{
		files:   map[string]string{},
		hits:    map[string]int{},
		status:  http.StatusOK,
		started: make(chan struct{}, 100),
	}

	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits[r.URL.Path]++
		body, found := s.files[r.URL.Path]
		status, delay, gate := s.status, s.delay, s.gate
		s.mu.Unlock()

		select {
		case s.started <- struct{}{}:
		default:
		}

		if gate != nil {
			<-gate
		}
		if delay > 0 {
			time.Sleep(delay)
		}

		if !found {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)

	return s
}

func (s *imageServer) put(path string, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[path] = content
}

func (s *imageServer) requests(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

func (s *imageServer) totalRequests() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := 0
	for _, h := range s.hits {
		n += h
	}
	return n
}

// hold makes the server wait, once a request arrives, until the returned
// function is called.
func (s *imageServer) hold() func() {
	s.mu.Lock()
	gate := make(chan struct{})
	s.gate = gate
	s.mu.Unlock()

	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}

func (s *imageServer) url(path string) string {
	return s.URL + path
}

// filepathToSlash makes paths comparable on every platform.
func filepathToSlash(p string) string {
	return filepath.ToSlash(p)
}

// holdLock takes a lock file, as another process would, and returns the
// function to release it.
func holdLock(t *testing.T, path string) func() {
	t.Helper()

	unlock, err := lockWithTimeout(path, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)

	return unlock
}

func lockWithTimeout(path string, timeout time.Duration) (func(), error) {
	return workspace.LockFile(path, timeout, nil)
}
