//go:build linux

package fsusage

import "syscall"

// Of returns the usage of the filesystem holding path.
func Of(path string) (Usage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Usage{}, err
	}
	bs := uint64(st.Bsize)
	return Usage{Total: st.Blocks * bs, Free: st.Bfree * bs, Avail: st.Bavail * bs}, nil
}
