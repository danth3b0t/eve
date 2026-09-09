package platform

import "golang.org/x/sys/unix"

func RequireLocalFilesystem(path string) error {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return pathError("E_STATE_IO", "cannot inspect state filesystem", path)
	}
	name := unix.ByteSliceToString(st.Fstypename[:])
	if st.Flags&unix.MNT_LOCAL == 0 || (name != "apfs" && name != "hfs") {
		return pathError("E_STATE_FILESYSTEM", "use local APFS or HFS+ for EVE state", path)
	}
	return nil
}
