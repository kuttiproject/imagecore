// Package imagecore manages the list of VM images, and the local cache of
// image files, for kutti drivers.
//
// The vbox, hyperv and qemu drivers used to carry near-identical copies of
// this code. imagecore holds the one copy. A driver describes itself with a
// Config, passes it to New, and gets a Store. The Store owns the list of
// images, which is kept in a file in the workspace config directory, and the
// image files, which are kept in a cache directory chosen by the driver.
//
// New does no I/O, and nothing in this package does any at import time. The
// first operation on a Store reads the list.
//
// Fetching, importing and purging an image are serialized, per version, between
// processes of the same user. The list is changed only under its own short
// lock, one image at a time, so that processes do not undo each other's
// changes.
//
// The Image type implements drivercore.Image. A driver declares
//
//	type Image = imagecore.Image
//
// so that its exported API does not change.
//
// # Compatibility
//
// The JSON format of the image list, the source URLs, and the cache paths and
// file names are the same as those used by the drivers before they adopted
// this package. Machines created by older versions of a driver refer to cached
// image files by absolute path, so these must not change.
package imagecore
