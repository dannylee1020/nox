//go:build darwin

package main

import (
	"syscall"
	"unsafe"
)

func isTerminal(fd uintptr) bool {
	var settings syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCGETA), uintptr(unsafe.Pointer(&settings)))
	return errno == 0
}
