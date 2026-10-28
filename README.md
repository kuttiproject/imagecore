# imagecore

Image list and image cache management shared by the kutti drivers (`driver-vbox`, `driver-hyperv` and `driver-qemu`).

A driver describes itself with an `imagecore.Config`, calls `imagecore.New`, and declares `type Image = imagecore.Image`.

See the package documentation for details.
