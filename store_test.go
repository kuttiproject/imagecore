package imagecore

import "testing"

// New must not call any of the functions in the Config, whether it is valid
// or not.
func TestNewCallsNoHooks(t *testing.T) {
	fail := func(name string) { t.Errorf("New called %s", name) }

	New(Config{
		DriverName:     "driver-qemu",
		ConfigFile:     "driver-qemu-images.json",
		ImageExtension: ".qcow2",
		DownloadSuffix: ".qcow2download",
		SourceURL:      func() string { fail("SourceURL"); return "" },
		CacheDir:       func() (string, error) { fail("CacheDir"); return "", nil },
		Unpack: func(string, string) (string, func(), error) {
			fail("Unpack")
			return "", nil, nil
		},
		InUse: func(string) (bool, error) { fail("InUse"); return false, nil },
	})

	// An invalid Config must not make New fail, either.
	if New(Config{}) == nil {
		t.Error("New returned nil for an empty Config")
	}
}
