//go:build !darwin

package main

import (
	"errors"
	"os"
)

func identityFromFileInfo(os.FileInfo) (objectIdentity, error) {
	return objectIdentity{}, errors.New("filesystem identity policy is supported only on Darwin")
}

func openNoFollow(string) (*os.File, error) {
	return nil, errors.New("direct no-follow open is supported only on Darwin")
}
