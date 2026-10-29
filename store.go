package imagecore

// A Store manages the image list and the cached image files of one driver.
// Create one with New.
//
// A Store keeps no copy of the image list in memory. Every operation reads
// the list file afresh, so that several kutti processes, each with its own
// Store, see each other's changes. Once created, a Store never changes, so it
// is safe for use by several goroutines.
type Store struct {
	config Config
}

// New creates a Store for the driver described by config. It does no I/O, and
// calls none of the functions in config. If config is invalid, which is a
// programming error, the first operation on the Store returns an error that
// says so.
func New(config Config) *Store {
	return &Store{config: config}
}
