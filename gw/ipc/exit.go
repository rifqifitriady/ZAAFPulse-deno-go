package ipc

// The gateway's exit codes (0 is a clean end: shutdown or the end of input). The supervisor in lib/ipc reads them;
// 1, 2, 4 and 5 stop it in the failed state instead of restarting (P4a E8).
const (
	// ExitDamaged: an I/O failure, a damaged outbox, or a stdin read error.
	ExitDamaged = 1
	// ExitRefused: the control plane refused the handshake, or a bad command line.
	ExitRefused = 2
	// ExitProtocol: a protocol error (after an "error" message).
	ExitProtocol = 3
	// ExitPortInUse: the main port is taken (the front proxy, P4a T4).
	ExitPortInUse = 4
	// ExitOutboxInUse: another process holds the outbox lock.
	ExitOutboxInUse = 5
)
