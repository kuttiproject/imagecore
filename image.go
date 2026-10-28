package imagecore

import (
	"encoding/json"

	"github.com/kuttiproject/drivercore"
)

// Image is a VM image: a template from which machines are created. It
// implements drivercore.Image.
//
// Images are created by a Store, and keep a reference to it.
type Image struct {
	store *Store

	k8sVersion string
	checksum   string
	sourceURL  string
	status     drivercore.ImageStatus
	deprecated bool
}

// imagedata is the serialized form of an Image. The field names, and their
// order, are part of the format of the image list, and are the same as those
// used by the drivers before imagecore. Do not change them.
type imagedata struct {
	ImageK8sVersion string
	ImageChecksum   string
	ImageSourceURL  string
	ImageStatus     drivercore.ImageStatus
	ImageDeprecated bool
}

// K8sVersion returns the version of Kubernetes present in the image.
func (i *Image) K8sVersion() string {
	return i.k8sVersion
}

// Status returns the status of the image. Status can be Downloaded, meaning
// the image exists in the local cache and can be used to create machines, or
// Notdownloaded, meaning it has to be downloaded using Fetch.
func (i *Image) Status() drivercore.ImageStatus {
	return i.status
}

// Deprecated returns true if the image's version of Kubernetes is deprecated.
// New machines should not be created from such an image.
func (i *Image) Deprecated() bool {
	return i.deprecated
}

// MarshalJSON returns the JSON encoding of the image.
func (i *Image) MarshalJSON() ([]byte, error) {
	return json.Marshal(imagedata{
		ImageK8sVersion: i.k8sVersion,
		ImageChecksum:   i.checksum,
		ImageSourceURL:  i.sourceURL,
		ImageStatus:     i.status,
		ImageDeprecated: i.deprecated,
	})
}

// UnmarshalJSON parses and restores a JSON-encoded image. If it fails, the
// image is not changed. Unknown fields are ignored, and missing fields are
// left empty.
func (i *Image) UnmarshalJSON(b []byte) error {
	var data imagedata

	err := json.Unmarshal(b, &data)
	if err != nil {
		return err
	}

	i.k8sVersion = data.ImageK8sVersion
	i.checksum = data.ImageChecksum
	i.sourceURL = data.ImageSourceURL
	i.status = data.ImageStatus
	i.deprecated = data.ImageDeprecated

	return nil
}
