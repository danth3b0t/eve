package files

import (
	"golang.org/x/sys/unix"
	"os"
)

func renameExclusive(parent *os.Root, from, to string) error {
	f, err := parent.OpenFile(".", os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return unix.Renameat2(int(f.Fd()), from, int(f.Fd()), to, unix.RENAME_NOREPLACE)
}
