package main

import "log"

// exitWith logs why the gateway stops on one line that starts with "fatal: ", and returns code. The supervisor
// (lib/ipc/supervisor.js) stops restarting only on an exit 1, 2, 4 or 5 that comes with such a line: a hard kill on
// Windows also exits 1, without one, and must be restarted (P4a E8).
func exitWith(logger *log.Logger, code int, format string, args ...any) int {
	logger.Printf("fatal: "+format, args...)
	return code
}
