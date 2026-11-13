//go:build !linux

package main

import "errors"

// run refuses: Landlock is Linux only, and the command never runs unlocked.
func run([]string) error {
	return errors.New("Landlock needs Linux; not running the command")
}
