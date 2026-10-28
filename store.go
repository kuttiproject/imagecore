package imagecore

import "sync"

// A Store manages the image list and the cached image files of one driver.
// Create one with New. A Store is safe for use by several goroutines.
type Store struct {
	config Config

	mu     sync.Mutex // guards images
	images map[string]*Image
}

// New creates a Store for the driver described by config. It does no I/O, and
// calls none of the functions in config. If config is invalid, which is a
// programming error, the first operation on the Store returns an error that
// says so.
func New(config Config) *Store {
	return &Store{config: config}
}
