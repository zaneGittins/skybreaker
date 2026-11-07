package system

import "syscall"

func diskCapacity(path string) (Capacity, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return Capacity{}, err
	}
	return Capacity{Total: s.Blocks * uint64(s.Bsize), Used: (s.Blocks - s.Bfree) * uint64(s.Bsize), Available: s.Bavail * uint64(s.Bsize)}, nil
}
