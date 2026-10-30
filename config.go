package imagecore

import (
	"fmt"
	"strings"
)

// Config describes how a driver stores its images. All the fields except
// Unpack and InUse are required.
//
// The functions in a Config are called when they are needed, never by New.
// They may be called from several goroutines at once.
type Config struct {
	// DriverName identifies the driver, for example "driver-qemu". It appears
	// in messages, and in the names of lock files, which are
	// "<DriverName>-images.lock" for the list, and
	// "<DriverName>-images-<version>.lock" for the fetch of one version.
	// It may contain only letters, digits, '-' and '_'.
	DriverName string

	// ConfigFile is the name of the file, in the workspace config directory,
	// that holds the local copy of the image list, for example
	// "driver-qemu-images.json". It is also the name of the file at the source
	// URL.
	ConfigFile string

	// ImageExtension is the extension, with its dot, of cached image files, for
	// example ".qcow2". The cached file for a version is named
	// "kutti-<version><ImageExtension>".
	ImageExtension string

	// DownloadSuffix is the ending, with its dot, of the name of the file an
	// image is downloaded into before it is checked and moved into the cache,
	// for example ".qcow2download". That file is named
	// "kutti-k8s-<version><DownloadSuffix>".
	DownloadSuffix string

	// SourceURL returns the location of the master image list. It is a
	// function because a driver may choose it per platform, and because tests
	// may change it after the Store has been created.
	SourceURL func() string

	// CacheDir returns the directory that holds cached image files, creating
	// it if necessary. Downloads, and files made by Unpack, are put in work
	// directories inside it, which are removed when the work is done. Note
	// that if this directory is shared by several users,
	// as it is for the qemu driver, the locks taken by a Store, which are
	// per user, do not serialize the users against each other.
	CacheDir func() (string, error)

	// Unpack, if not nil, is called on every image file before it is checked
	// and cached, whether it was downloaded or given to Image.FromFile. It
	// returns the path of the file to check and cache, and a function to call
	// when that file is no longer needed, which may be nil. workdir is a
	// directory in which Unpack may create files, which is removed with
	// everything in it when the image has been dealt with. If Unpack is nil,
	// the file is used as it is.
	Unpack func(path string, workdir string) (imagepath string, cleanup func(), err error)

	// InUse, if not nil, reports whether the cached image for a version is in
	// use by any machine. If it is set, a cached image is not replaced with
	// different content, or removed, while it is in use, and an image already
	// cached with the right checksum is not copied again. If it is nil,
	// images are never considered to be in use.
	InUse func(k8sversion string) (bool, error)
}

// validate checks that the required fields are present and usable.
func (c *Config) validate() error {
	var problems []string

	if !validName(c.DriverName) {
		problems = append(problems, "DriverName must be non-empty, and use only letters, digits, '-' and '_'")
	}
	if !validFileName(c.ConfigFile) {
		problems = append(problems, "ConfigFile must be a plain file name")
	}
	if !validSuffix(c.ImageExtension) {
		problems = append(problems, "ImageExtension must be a dot followed by letters, digits, '.', '-' or '_'")
	}
	if !validSuffix(c.DownloadSuffix) {
		problems = append(problems, "DownloadSuffix must be a dot followed by letters, digits, '.', '-' or '_'")
	}
	if c.SourceURL == nil {
		problems = append(problems, "SourceURL must be set")
	}
	if c.CacheDir == nil {
		problems = append(problems, "CacheDir must be set")
	}

	if len(problems) == 0 {
		return nil
	}

	return fmt.Errorf("imagecore: invalid Config: %s", strings.Join(problems, "; "))
}

func isAlphaNumeric(r byte) bool {
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9')
}

// validName accepts letters, digits, '-' and '_', starting with a letter or
// digit.
func validName(s string) bool {
	if s == "" || !isAlphaNumeric(s[0]) {
		return false
	}

	for i := 0; i < len(s); i++ {
		if !isAlphaNumeric(s[i]) && s[i] != '-' && s[i] != '_' {
			return false
		}
	}

	return true
}

// validFileName accepts letters, digits, '.', '-' and '_', starting with a
// letter or digit. That leaves out path separators, drive prefixes, and the
// names "." and "..".
func validFileName(s string) bool {
	if s == "" || !isAlphaNumeric(s[0]) {
		return false
	}

	for i := 0; i < len(s); i++ {
		if !isAlphaNumeric(s[i]) && s[i] != '.' && s[i] != '-' && s[i] != '_' {
			return false
		}
	}

	return true
}

// validSuffix accepts a dot followed by a valid file name.
func validSuffix(s string) bool {
	return strings.HasPrefix(s, ".") && validFileName(s[1:])
}
