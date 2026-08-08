//go:build !darwin

package main

import "errors"

func execReviewed(string, []string, []string, string) error {
	return errors.New("exec policy is supported only on Darwin")
}
