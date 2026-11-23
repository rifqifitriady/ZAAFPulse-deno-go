//go:build windows

package front

import (
	"errors"
	"syscall"
)

// WSAEADDRINUSE, and WSAEACCES, which Windows returns when another socket holds the port exclusively.
const (
	wsaeaddrinuse syscall.Errno = 10048
	wsaeacces     syscall.Errno = 10013
)

func addrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, wsaeaddrinuse) || errors.Is(err, wsaeacces)
}
