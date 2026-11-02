# imagecore

Image list and image cache management shared by the kutti drivers (`driver-vbox`, `driver-hyperv` and `driver-qemu`).

The three drivers used to carry near-identical copies of this code. `imagecore` holds the one copy, and fixes in it once. A driver describes itself with a `Config`, calls `New`, and declares `type Image = imagecore.Image`, so its exported API does not change.

## Using it

```go
store := imagecore.New(imagecore.Config{
	DriverName:     "driver-qemu",
	ConfigFile:     "driver-qemu-images.json",
	ImageExtension: ".qcow2",
	DownloadSuffix: ".qcow2download",
	SourceURL:      func() string { return ImagesSourceURL },
	CacheDir:       cacheDir,
	InUse:          isImageInUse, // optional
	// Unpack: ...,              // optional
})

err := store.UpdateImageList()
versions := store.K8sVersions()
image, err := store.GetImage("1.35")
err = image.Fetch()
```

`New` does no I/O, and calls none of the functions in the `Config`. A `Store` keeps nothing in memory: every call reads the list file afresh, so several kutti processes see each other's changes. A `Store` never changes after `New`, and is safe for use by several goroutines. An `Image` is a snapshot of one entry of the list, and is not; get a fresh one for each goroutine.

`Store.ImagePath(version)` returns where the cached file for a version is, or would be. A driver uses it, and not its own naming code, to find the image from which to create machines.

## What the drivers differ in

| `Config`               | vbox                     | hyperv                  | qemu                          |
|------------------------|--------------------------|-------------------------|-------------------------------|
| `ImageExtension`       | `.ova`                   | `.vhdx`                 | `.qcow2`                      |
| `DownloadSuffix`       | `.ovadownload`           | `.download.zip`         | `.qcow2download`              |
| `Unpack`               | none                     | unzip a single-file zip | none                          |
| `InUse`                | none                     | PowerShell, disk parent | `qemu-img info`               |
| `CacheDir`             | per user                 | per user                | shared, `/var/tmp/kutti/...`  |

The `InUse` function also switches on a short-circuit: an image that is already cached with the right checksum is not copied again. Without it, an image is always copied, which is right for vbox, as it copies the OVA when a machine is created.

## Behaviour

**The list.** The local list is `ConfigFile` in the workspace config directory. A missing file is an empty list, and reading never writes. `UpdateImageList` downloads the master list, parses and checks it in memory, and only then replaces the local list. An image that was `Downloaded` stays so if the new list has the same checksum and source URL for it. Every other image is `Notdownloaded`, whatever the master list says.

**Bad lists.** A version must match `^[A-Za-z0-9][A-Za-z0-9._+-]*$`, be at most 64 characters long, and be the key its entry is stored under, because versions go into file names. One invalid entry rejects the whole list. A downloaded list that is rejected is logged at `kuttilog.Error`, and the local list is kept as it was. A local list that cannot be used is reported as an error, and left alone; `UpdateImageList` is the way to repair it, and moves the damaged file aside as `<name>.<timestamp>.corrupt`. A list that cannot be read for any other reason is never replaced.

**Fetching.** An image is downloaded into a work directory inside the cache directory, unpacked if the driver needs it, checked against the checksum in the list, and copied into the cache as `kutti-<version><ImageExtension>`. Work directories are removed afterwards. A killed process leaves its work directory behind, and a later fetch of the same version removes ones that are over a day old.

**Locks.** Fetching, importing and purging the image for one version are serialized between processes of the same user, by `<DriverName>-images-<version>.lock` in the config directory. A process that waits for another's fetch of the same version does nothing more if the image has become `Downloaded`. The list is changed only under `<DriverName>-images.lock`, one image at a time, and only if the entry still has the checksum and source URL of the image, so processes do not undo each other's changes. Locks are per user. For qemu, whose cache is shared by all users of a machine, two users fetching at once are not serialized against each other.

**Lists that change under an image.** If the list is updated after an `Image` was obtained, and its entry has changed, `Fetch` and `FromFile` fail at once with a message to get the image again. If the list changes during the work, the file stays in the cache, the error says so, and the image is not marked `Downloaded`.

## Why `Config` is a struct

The drivers differ in data, not in behaviour. Of the eight things that vary, four are constants, two are functions that must be evaluated late, and two are optional hooks. A struct says that directly.

- **Adding a setting does not break anyone.** A new field in a struct that drivers fill in by name changes nothing for them. A new method on an interface breaks every implementation, in three repositories that are released separately.
- **Absent is `nil`.** `Unpack` and `InUse` are optional, and the behaviour that depends on them is switched by whether they are set. With an interface this becomes a type assertion for optional interfaces, which hides the rule in the code.
- **It is a value.** `New` copies it, and it can be checked in one place. A test builds a variant by changing one field, rather than writing a new type for each case.
- **Behaviour that must vary is already a function.** `SourceURL`, `CacheDir`, `Unpack` and `InUse` are the places where a driver supplies code. If a driver ever needs more than that, an interface can be added next to the struct without breaking it.

## Compatibility

The JSON format of the list, the source URLs, and the cache paths and file names are those of the drivers before they adopted this package. Hyperv and qemu differencing disks refer to the cached image by absolute path, so these must not change. `profiles_test.go` holds the per-driver tests that pin them down; a driver's migration has to pass them.

## Testing

```
go test ./...
```

Most tests need nothing but the module. They use `httptest` servers and a temporary workspace, and replace the workspace and logging, so they must not run in parallel. Until `workspace` v0.3.2, which has `LockFile`, is released, build against a local `workspace` with a `go.work` file.

The tests of locking, and of replacing and removing files, behave differently on Windows, and should be run there too.
