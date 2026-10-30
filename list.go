package imagecore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/kuttiproject/drivercore"
	"github.com/kuttiproject/kuttilog"
	"github.com/kuttiproject/workspace"
)

// How long an operation waits for another process to finish with the image
// list. The list is small, and the lock is held only to read, change and save
// it, so waiting this long means something is wrong. It is a variable so that
// tests can shorten it.
var listLockTimeout = 30 * time.Second

// maxListSize is the largest image list file that will be accepted from a
// download. Real lists are about a kilobyte.
const maxListSize = 4 << 20

// invalidListError reports that the local copy of the image list exists, and
// could be read, but cannot be used: it is not valid JSON, or one of its
// entries is not valid. Other errors, such as not being allowed to read the
// file, are not of this type.
type invalidListError struct {
	err error
}

func (e *invalidListError) Error() string { return e.err.Error() }
func (e *invalidListError) Unwrap() error { return e.err }

// listPath returns the path of the local copy of the image list. It fails if
// the Config is invalid.
func (s *Store) listPath() (string, error) {
	if err := s.config.validate(); err != nil {
		return "", err
	}

	dir, err := workspace.ConfigDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, s.config.ConfigFile), nil
}

// parseList parses image list data, checks every entry, and makes the images
// belong to this Store. source says where the data came from, for messages.
func (s *Store) parseList(data []byte, source string) (map[string]*Image, error) {
	var list map[string]*Image

	err := json.Unmarshal(data, &list)
	if err != nil {
		return nil, fmt.Errorf("imagecore: %s could not be parsed: %w", source, err)
	}

	// A JSON null is a valid, empty list.
	if list == nil {
		list = map[string]*Image{}
	}

	err = checkImageList(source, list)
	if err != nil {
		return nil, err
	}

	for _, img := range list {
		img.store = s
	}

	return list, nil
}

// readList reads the local copy of the image list. A list file that does not
// exist is an empty list. A list that exists but cannot be used gives an
// *invalidListError, and the file is not touched.
func (s *Store) readList(path string) (map[string]*Image, error) {
	data, err := workspace.ReadFileRetrying(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]*Image{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("imagecore: could not read the image list file '%s': %w", path, err)
	}

	list, err := s.parseList(data, fmt.Sprintf("the image list file '%s'", path))
	if err != nil {
		return nil, &invalidListError{err}
	}

	return list, nil
}

// images returns the images in the local list, in version order.
func (s *Store) images() ([]*Image, error) {
	path, err := s.listPath()
	if err != nil {
		return nil, err
	}

	list, err := s.readList(path)
	if err != nil {
		return nil, err
	}

	versions := make([]string, 0, len(list))
	for version := range list {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool {
		return compareVersions(versions[i], versions[j]) < 0
	})

	result := make([]*Image, len(versions))
	for i, version := range versions {
		result[i] = list[version]
	}

	return result, nil
}

// ListImages returns the images in the local image list, in version order. It
// returns an empty slice and an error if the list cannot be read.
func (s *Store) ListImages() ([]drivercore.Image, error) {
	images, err := s.images()
	if err != nil {
		return []drivercore.Image{}, err
	}

	result := make([]drivercore.Image, len(images))
	for i, img := range images {
		result[i] = img
	}

	return result, nil
}

// GetImage returns the image for a version of Kubernetes. It returns a nil
// image and an error if the list cannot be read, or has no such image.
func (s *Store) GetImage(k8sversion string) (drivercore.Image, error) {
	path, err := s.listPath()
	if err != nil {
		return nil, err
	}

	list, err := s.readList(path)
	if err != nil {
		return nil, err
	}

	img, ok := list[k8sversion]
	if !ok {
		return nil, fmt.Errorf("no image present for K8s version %s", k8sversion)
	}

	return img, nil
}

// ValidK8sVersion returns true if the specified Kubernetes version is in the
// image list. It returns false if the list cannot be read, and logs why.
func (s *Store) ValidK8sVersion(k8sversion string) bool {
	images, err := s.images()
	if err != nil {
		kuttilog.Printf(kuttilog.Error, "%v", err)
		return false
	}

	for _, img := range images {
		if img.k8sVersion == k8sversion {
			return true
		}
	}

	return false
}

// K8sVersions returns the Kubernetes versions in the image list, in version
// order: "1.9" comes before "1.10". It returns an empty slice if the list
// cannot be read, and logs why.
func (s *Store) K8sVersions() []string {
	images, err := s.images()
	if err != nil {
		kuttilog.Printf(kuttilog.Error, "%v", err)
		return []string{}
	}

	result := make([]string, len(images))
	for i, img := range images {
		result[i] = img.k8sVersion
	}

	return result
}

// UpdateImageList downloads the master image list, and makes it the local
// list. An image that is Downloaded stays Downloaded if the new list has an
// image for the same version with the same checksum and source URL. Every
// other image in the new list is Notdownloaded, whatever status the master
// list gives it. Images that are not in the new list are dropped from the
// list, but their cached files are not touched.
//
// The downloaded data is checked before anything changes. If it cannot be
// parsed, or any entry is invalid, the whole list is rejected, an error is
// logged and returned, and the local list is left as it was.
//
// If the local list cannot be used, because its content is invalid, it is
// moved aside as "<name>.<timestamp>.corrupt", in the same directory, and
// replaced. UpdateImageList is how a damaged list is repaired.
func (s *Store) UpdateImageList() error {
	path, err := s.listPath()
	if err != nil {
		return err
	}

	url := s.config.SourceURL()
	kuttilog.Println(kuttilog.Info, "Fetching image list...")

	data, err := s.downloadList(filepath.Dir(path), url)
	if err != nil {
		return err
	}

	newlist, err := s.parseList(data, fmt.Sprintf("the image list downloaded from %s", url))
	if err != nil {
		kuttilog.Printf(kuttilog.Error, "%v. The whole list was rejected, and the current list has been kept.", err)
		return err
	}

	return s.replaceList(path, newlist)
}

// downloadList downloads url into a temporary file in dir, and returns its
// content. The temporary file is always removed.
func (s *Store) downloadList(dir string, url string) ([]byte, error) {
	temp, err := os.CreateTemp(dir, s.config.ConfigFile+".*.new")
	if err != nil {
		return nil, err
	}
	temppath := temp.Name()
	temp.Close()
	defer os.Remove(temppath)

	kuttilog.Printf(kuttilog.Debug, "Fetching from %v into %v.", url, temppath)

	err = workspace.DownloadFile(url, temppath)
	kuttilog.Printf(kuttilog.Debug, "Error: %v", err)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(temppath)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxListSize {
		err = fmt.Errorf("imagecore: the image list downloaded from %s is too large (%d bytes)", url, info.Size())
		kuttilog.Printf(kuttilog.Error, "%v. The current list has been kept.", err)
		return nil, err
	}

	return workspace.ReadFileRetrying(temppath)
}

// lockList takes the lock that serializes changes to the local image list
// between processes of the same user, and returns the function that releases
// it. The lock file is in the config directory, next to the list.
func (s *Store) lockList(path string) (func(), error) {
	lockpath := filepath.Join(filepath.Dir(path), s.config.DriverName+"-images.lock")

	unlock, err := workspace.LockFile(lockpath, listLockTimeout, func() {
		kuttilog.Println(kuttilog.Info, "Waiting for another kutti process to finish with the image list...")
	})
	if err != nil {
		return nil, fmt.Errorf("imagecore: could not lock the image list: %w", err)
	}

	return unlock, nil
}

// replaceList makes newlist the local list, keeping the Downloaded status of
// images that are unchanged. It reads, changes and saves the local list under
// the list lock.
func (s *Store) replaceList(path string, newlist map[string]*Image) error {
	unlock, err := s.lockList(path)
	if err != nil {
		return err
	}
	defer unlock()

	oldlist, err := s.readList(path)

	var invalid *invalidListError
	if errors.As(err, &invalid) {
		backup, asideerr := setAside(path)
		if asideerr != nil {
			return fmt.Errorf("%w, and it could not be set aside: %v", err, asideerr)
		}

		// readList returned no list, so there is nothing to carry over.
		kuttilog.Printf(kuttilog.Error, "%v. It has been moved to '%s', and is being replaced.", err, backup)
	} else if err != nil {
		return err
	}

	for version, img := range newlist {
		img.status = drivercore.ImageStatusNotDownloaded

		old := oldlist[version]
		if old != nil &&
			old.checksum == img.checksum &&
			old.sourceURL == img.sourceURL &&
			old.status == drivercore.ImageStatusDownloaded {
			img.status = drivercore.ImageStatusDownloaded
		}
	}

	data, err := json.Marshal(newlist)
	if err != nil {
		return err
	}

	return workspace.AtomicWriteFile(path, data)
}

// setAside renames a file to "<path>.<timestamp>.corrupt", and returns the new
// name. It never replaces an existing file. If the file does not exist any
// more, that is not an error.
func setAside(path string) (string, error) {
	base := fmt.Sprintf("%s.%s", path, time.Now().UTC().Format("20060102T150405.000Z"))

	backup := base + ".corrupt"
	for n := 1; ; n++ {
		_, err := os.Lstat(backup)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return "", err
		}
		if n >= 100 {
			return "", fmt.Errorf("no free name for the set-aside copy of '%s'", path)
		}

		backup = fmt.Sprintf("%s-%d.corrupt", base, n)
	}

	err := os.Rename(path, backup)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	return backup, nil
}

// errImageChanged is returned by setStatus when the entry in the list is no
// longer for the image it was asked to change.
var errImageChanged = errors.New("imagecore: the image in the image list has changed or been removed")

// setStatus changes the status of one image in the local list. It reads the
// list again, under the list lock, and changes that image only, so it does not
// undo what another process has done to the list in the meantime. It fails
// with errImageChanged if the list has no entry for the version, or the entry
// is for a different image, that is, has another checksum or source URL, than
// img. If the list cannot be used, it fails and leaves the file alone.
func (s *Store) setStatus(img *Image, status drivercore.ImageStatus) error {
	path, err := s.listPath()
	if err != nil {
		return err
	}

	unlock, err := s.lockList(path)
	if err != nil {
		return err
	}
	defer unlock()

	list, err := s.readList(path)
	if err != nil {
		return err
	}

	entry := list[img.k8sVersion]
	if !img.sameAs(entry) {
		return errImageChanged
	}

	if entry.status == status {
		return nil
	}
	entry.status = status

	data, err := json.Marshal(list)
	if err != nil {
		return err
	}

	return workspace.AtomicWriteFile(path, data)
}
