package platform

import "golang.org/x/sys/unix"

func RequireLocalFilesystem(path string) error {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return pathError("E_STATE_IO", "cannot inspect state filesystem", path)
	}
	// Deliberately bounded support, not a claim about arbitrary FUSE/network
	// filesystems. Overlay support assumes a local, locking-capable backing store.
	switch st.Type {
	case 0xef53, 0x58465342, 0x9123683e, 0x2fc12fc1, 0x01021994, 0x794c7630, 0xf2f52010:
		// ext2/3/4, XFS, btrfs, ZFS, tmpfs, overlayfs, F2FS.
		return nil
	default:
		return pathError("E_STATE_FILESYSTEM", "use a supported local filesystem for EVE state", path)
	}
}
