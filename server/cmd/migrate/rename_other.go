//go:build !linux && !darwin

package main

import (
	"os"
)

func renameNoReplace(from, to string) error {
	if err := os.Link(from, to); err != nil {
		return err
	}
	// The no-replace link is the atomic publication point. Leaving an adjacent
	// staging link for the caller's cleanup is safer than removing a fully
	// published destination if unlinking the staging name is interrupted.
	_ = os.Remove(from)
	return nil
}
