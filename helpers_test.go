package imagecore

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/kuttiproject/kuttilog"
	"github.com/kuttiproject/workspace"
)

// testWorkspace points the workspace at a new temporary directory for the
// duration of a test, and returns its config directory. The workspace is
// global, so tests that use it must not run in parallel.
func testWorkspace(t *testing.T) string {
	t.Helper()

	if err := workspace.Set(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(workspace.Reset)

	// Keep what is logged off the test output. A test that wants to look at
	// the log calls captureLog itself, which takes over.
	captureLog(t)

	dir, err := workspace.ConfigDir()
	if err != nil {
		t.Fatal(err)
	}

	return dir
}

// newTestStore returns a Store with the settings of the qemu driver, and the
// path of its list file. The Store gets its source URL from *url, when it is
// asked, so a test can change it after the Store is created.
func newTestStore(t *testing.T, url *string) (*Store, string) {
	t.Helper()

	config := validConfig()
	config.SourceURL = func() string { return *url }

	store := New(config)
	path, err := store.listPath()
	if err != nil {
		t.Fatal(err)
	}

	return store, path
}

// listServer serves one image list, which a test can change.
type listServer struct {
	*httptest.Server

	mu     sync.Mutex
	body   []byte
	status int
	hits   int
}

func newListServer(t *testing.T, body string) *listServer {
	t.Helper()

	ls := &listServer{body: []byte(body), status: http.StatusOK}
	ls.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ls.mu.Lock()
		defer ls.mu.Unlock()

		ls.hits++
		w.WriteHeader(ls.status)
		w.Write(ls.body)
	}))
	t.Cleanup(ls.Close)

	return ls
}

func (ls *listServer) serve(body string) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	ls.body = []byte(body)
}

func (ls *listServer) failWith(status int) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	ls.status = status
}

func (ls *listServer) requests() int {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.hits
}

// logCapture is a kuttilog logger that records what is logged. Every line
// starts with the level in angle brackets, so "<-1>" is kuttilog.Error.
type logCapture struct {
	mu    sync.Mutex
	lines []string
}

func captureLog(t *testing.T) *logCapture {
	t.Helper()

	c := &logCapture{}
	kuttilog.SetLogger(c)
	t.Cleanup(kuttilog.ResetLogger)

	return c
}

func (c *logCapture) MaxLevel() int { return kuttilog.Debug }

func (c *logCapture) LevelPrefix(level int) string { return fmt.Sprintf("<%d>", level) }

func (c *logCapture) add(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, line)
}

func (c *logCapture) Print(v ...interface{}) { c.add(fmt.Sprint(v...)) }

func (c *logCapture) Printf(format string, v ...interface{}) { c.add(fmt.Sprintf(format, v...)) }

func (c *logCapture) Println(v ...interface{}) {
	c.add(strings.TrimSuffix(fmt.Sprintln(v...), "\n"))
}

// errors returns the lines logged at kuttilog.Error.
func (c *logCapture) errors() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	var result []string
	for _, line := range c.lines {
		if strings.HasPrefix(line, "<-1> ") {
			result = append(result, line)
		}
	}

	return result
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o666); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

// dirNames returns the sorted names of the entries of a directory.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	sort.Strings(names)

	return names
}

// corruptFiles returns the paths of the set-aside copies of a list file.
func corruptFiles(t *testing.T, listpath string) []string {
	t.Helper()

	matches, err := filepath.Glob(listpath + ".*.corrupt")
	if err != nil {
		t.Fatal(err)
	}

	return matches
}

func testdata(t *testing.T, name string) string {
	t.Helper()
	return readFile(t, filepath.Join("testdata", name))
}
