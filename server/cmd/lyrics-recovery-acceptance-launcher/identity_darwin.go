//go:build darwin

package main

import (
	"errors"
	"os"
	"syscall"
)

func identityFromFileInfo(info os.FileInfo) (objectIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return objectIdentity{}, errors.New("filesystem identity is unavailable")
	}
	return objectIdentity{
		Device:         uint64(stat.Dev),
		Inode:          stat.Ino,
		UID:            stat.Uid,
		GID:            stat.Gid,
		Mode:           portableMode(info.Mode()),
		LinkCount:      uint64(stat.Nlink),
		Size:           stat.Size,
		ModificationNS: stat.Mtimespec.Sec*1_000_000_000 + stat.Mtimespec.Nsec,
	}, nil
}

func openNoFollow(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		syscall.Close(fd)
		return nil, errors.New("could not bind file descriptor")
	}
	return file, nil
}

func portableMode(mode os.FileMode) uint32 {
	result := uint32(mode.Perm())
	if mode&os.ModeSetuid != 0 {
		result |= 0o4000
	}
	if mode&os.ModeSetgid != 0 {
		result |= 0o2000
	}
	if mode&os.ModeSticky != 0 {
		result |= 0o1000
	}
	return result
}
