//go:build !darwin && !linux

package main

func isTerminal(uintptr) bool {
	return false
}
