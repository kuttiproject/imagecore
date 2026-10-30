package imagecore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kuttiproject/drivercore"
	"github.com/kuttiproject/kuttilog"
	"github.com/kuttiproject/workspace"
)

var _ drivercore.Image = (*Image)(nil)

// attached returns the Store the image came from. An image that did not come
// from a Store, for example one decoded from JSON, cannot be fetched.
func (i *Image) attached() (*Store, error) {
	if i.store == nil {
		return nil, errors.New("imagecore: the image does not belong to a Store; get it with GetImage or ListImages")
	}

	return i.store, nil
}

// sameAs reports whether an entry of the image list, looked up by this
// image's version, is for the same image: from the same source, with the same
// content. A list is checked when it is read, so an entry always has the
// version it is listed under.
func (i *Image) sameAs(entry *Image) bool {
	return entry != nil &&
		entry.checksum == i.checksum &&
		entry.sourceURL == i.sourceURL
}

// currentEntry returns the entry in the local image list for this image. It
// fails if the list has no entry for the version, or the entry is for a
// different image now, because the list was updated after this Image was
// obtained.
func (s *Store) currentEntry(i *Image) (*Image, error) {
	path, err := s.listPath()
	if err != nil {
		return nil, err
	}

	list, err := s.readList(path)
	if err != nil {
		return nil, err
	}

	entry := list[i.k8sVersion]
	if !i.sameAs(entry) {
		return nil, fmt.Errorf(
			"imagecore: the image for Kubernetes %s has changed or been removed in the image list; get the image again",
			i.k8sVersion,
		)
	}

	return entry, nil
}

// Fetch downloads the image from its source URL into the local cache.
//
// Only one process of a user fetches, imports or purges the image for a
// version at a time. If the image becomes Downloaded while Fetch waits its
// turn, because another process fetched it, Fetch does nothing more. If the
// image was already Downloaded when it was obtained, Fetch downloads it again,
// which repairs a cached file that has gone missing.
func (i *Image) Fetch() error {
	return i.fetch(nil)
}

// FetchWithProgress is like Fetch, and reports the progress of the download
// via the supplied callback. The callback reports current and total in bytes.
func (i *Image) FetchWithProgress(progress func(current int64, total int64)) error {
	return i.fetch(progress)
}

func (i *Image) fetch(progress func(int64, int64)) error {
	s, err := i.attached()
	if err != nil {
		return err
	}

	unlock, err := s.lockVersion(i.k8sVersion)
	if err != nil {
		return err
	}
	defer unlock()

	current, err := s.currentEntry(i)
	if err != nil {
		return err
	}

	if i.status != drivercore.ImageStatusDownloaded && current.status == drivercore.ImageStatusDownloaded {
		kuttilog.Printf(kuttilog.Info, "The image for Kubernetes %s has been downloaded by another process.", i.k8sVersion)
		i.status = drivercore.ImageStatusDownloaded
		return nil
	}

	cachedir, err := s.config.CacheDir()
	if err != nil {
		return err
	}

	removeStaleWorkDirs(cachedir, i.k8sVersion)

	workdir, err := newWorkDir(cachedir, i.k8sVersion)
	if err != nil {
		return err
	}
	defer os.RemoveAll(workdir)

	downloadpath := filepath.Join(workdir, "kutti-k8s-"+i.k8sVersion+s.config.DownloadSuffix)

	kuttilog.Printf(kuttilog.Info, "Downloading image for Kubernetes %s...", i.k8sVersion)
	kuttilog.Printf(kuttilog.Debug, "Downloading from %s to %s", i.sourceURL, downloadpath)

	if progress != nil {
		err = workspace.DownloadFileWithProgress(i.sourceURL, downloadpath, progress)
	} else {
		err = workspace.DownloadFile(i.sourceURL, downloadpath)
	}
	if err != nil {
		return err
	}

	kuttilog.Printf(kuttilog.Debug, "Downloaded image to temp file %s, adding to cache", downloadpath)

	err = s.importFile(i, downloadpath, workdir)
	if err != nil {
		return err
	}

	kuttilog.Printf(kuttilog.Info, "Finished downloading image for Kubernetes %s.", i.k8sVersion)
	return nil
}

// FromFile imports an image from a file on the local file system into the
// local cache. The file is unpacked, if the driver needs that, and its
// checksum must be the one in the image list.
//
// If the driver can tell whether a cached image is in use by a machine, a
// cached image is not replaced with different content while it is, and a
// cached image that already has the right checksum is not copied again.
func (i *Image) FromFile(filepath string) error {
	s, err := i.attached()
	if err != nil {
		return err
	}

	unlock, err := s.lockVersion(i.k8sVersion)
	if err != nil {
		return err
	}
	defer unlock()

	_, err = s.currentEntry(i)
	if err != nil {
		return err
	}

	return s.importFile(i, filepath, "")
}

// importFile checks an image file, copies it into the cache, and marks the
// image Downloaded in the list. The caller holds the version lock, and has
// checked that the list entry is still for this image. workdir is the work
// directory of the caller, in which Unpack may work, or "" if there is none,
// in which case one is made if it is needed.
func (s *Store) importFile(i *Image, srcpath string, workdir string) error {
	if s.config.Unpack != nil {
		if workdir == "" {
			cachedir, err := s.config.CacheDir()
			if err != nil {
				return err
			}

			workdir, err = newWorkDir(cachedir, i.k8sVersion)
			if err != nil {
				return err
			}
			defer os.RemoveAll(workdir)
		}

		unpacked, cleanup, err := s.config.Unpack(srcpath, workdir)
		if cleanup != nil {
			defer cleanup()
		}
		if err != nil {
			return err
		}
		if unpacked == "" {
			return fmt.Errorf("imagecore: %s: Unpack returned no file", s.config.DriverName)
		}

		srcpath = unpacked
	}

	kuttilog.Println(kuttilog.Info, "Checking image validity...")

	filechecksum, err := workspace.ChecksumFile(srcpath)
	if err != nil {
		return err
	}

	if filechecksum != i.checksum {
		kuttilog.Printf(kuttilog.Debug, "checksum for file %v failed.\nWanted: %v\nGot   : %v\n", srcpath, i.checksum, filechecksum)
		return errors.New("file is not valid")
	}

	destpath, err := s.ImagePath(i.k8sVersion)
	if err != nil {
		return err
	}

	alreadycached := false

	// If a cached copy of this image already exists, machines may already
	// have differencing disks backed by it. Overwriting it in place with
	// different content would silently corrupt every one of those disks.
	if s.config.InUse != nil {
		if existingchecksum, err := workspace.ChecksumFile(destpath); err == nil {
			if existingchecksum == i.checksum {
				kuttilog.Printf(kuttilog.Debug, "Image %s is already in the cache.", destpath)
				alreadycached = true
			} else {
				inuse, err := s.config.InUse(i.k8sVersion)
				if err != nil {
					return fmt.Errorf(
						"cannot replace cached image for K8s version %s: could not verify it is unused: %v",
						i.k8sVersion, err,
					)
				}
				if inuse {
					return fmt.Errorf(
						"cannot replace cached image for K8s version %s: it is in use as a backing disk by one or more machines",
						i.k8sVersion,
					)
				}
			}
		}
	}

	if !alreadycached {
		kuttilog.Println(kuttilog.Info, "Copying image to local cache...")

		err = workspace.CopyFile(srcpath, destpath, copyBufferSize, true)
		if err != nil {
			return err
		}
	}

	err = s.setStatus(i, drivercore.ImageStatusDownloaded)
	if errors.Is(err, errImageChanged) {
		return fmt.Errorf(
			"imagecore: the image list changed while the image for Kubernetes %s was being added. "+
				"The image file is in the cache, but is not marked as downloaded; get the image again",
			i.k8sVersion,
		)
	}
	if err != nil {
		return err
	}

	i.status = drivercore.ImageStatusDownloaded
	return nil
}

// PurgeLocal removes the image from the local cache, and marks it
// Notdownloaded. It does nothing if the image list does not have the image
// as Downloaded, and succeeds if the cached file is already gone.
//
// If the driver can tell whether a cached image is in use by a machine, a
// cached image that is in use is not removed, and PurgeLocal fails.
func (i *Image) PurgeLocal() error {
	s, err := i.attached()
	if err != nil {
		return err
	}

	unlock, err := s.lockVersion(i.k8sVersion)
	if err != nil {
		return err
	}
	defer unlock()

	listpath, err := s.listPath()
	if err != nil {
		return err
	}

	list, err := s.readList(listpath)
	if err != nil {
		return err
	}

	// What counts is the list as it is now, not as it was when this Image was
	// obtained: another process may have fetched or purged it since.
	entry := list[i.k8sVersion]
	if entry == nil {
		return nil
	}
	i.status = entry.status
	if entry.status != drivercore.ImageStatusDownloaded {
		return nil
	}

	kuttilog.Printf(kuttilog.Info, "Purging cached image for Kubernetes %s...", i.k8sVersion)

	if s.config.InUse != nil {
		inuse, err := s.config.InUse(i.k8sVersion)
		if err != nil {
			return fmt.Errorf(
				"cannot remove cached image for K8s version %s: could not verify it is unused: %v",
				i.k8sVersion, err,
			)
		}
		if inuse {
			return fmt.Errorf(
				"cannot remove cached image for K8s version %s: it is in use as a backing disk by one or more machines",
				i.k8sVersion,
			)
		}
	}

	destpath, err := s.ImagePath(i.k8sVersion)
	if err != nil {
		return err
	}

	kuttilog.Printf(kuttilog.Debug, "Deleting image file: %s", destpath)

	err = workspace.RemoveFile(destpath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	// If the entry changed since it was read, the update that changed it has
	// already made the image Notdownloaded, and there is nothing to save.
	err = s.setStatus(entry, drivercore.ImageStatusNotDownloaded)
	if err != nil && !errors.Is(err, errImageChanged) {
		return err
	}

	i.status = drivercore.ImageStatusNotDownloaded
	return nil
}
