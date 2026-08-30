//go:build darwin

package main

import (
	"errors"
	"os/signal"
	"syscall"
)

func execReviewed(path string, argv, environment []string, workingDirectory string) error {
	if err := syscall.Chdir(workingDirectory); err != nil {
		return err
	}
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		return err
	}
	if limit.Cur > 1<<20 {
		return errors.New("open-file limit exceeds the reviewed descriptor-closure bound")
	}
	for descriptor := 3; uint64(descriptor) < limit.Cur; descriptor++ {
		syscall.CloseOnExec(descriptor)
	}
	syscall.Umask(0o077)
	signal.Reset()
	return syscall.Exec(path, argv, environment)
}
