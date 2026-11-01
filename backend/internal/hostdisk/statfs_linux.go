package hostdisk

import "syscall"

func statfs(path string) (Space, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Space{}, err
	}
	bs := uint64(st.Bsize) //nolint:gosec // a block size is positive
	return Space{Total: st.Blocks * bs, Free: st.Bavail * bs}, nil
}
