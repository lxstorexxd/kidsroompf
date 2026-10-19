//go:build linux

package mediamtx

import (
	"os/exec"
	"syscall"
)

// applyProcAttr ties mediamtx's life to the agent's.
//
// Stop and Restart kill the child themselves, but neither runs when the agent
// is killed outright — SIGKILL, or an out-of-memory kill. mediamtx would then
// keep running, keep holding its RTSP and HLS ports, and the restarted agent
// would never be able to start its own copy: the stream would stay dead until
// someone logged in and killed the orphan by hand.
//
// Pdeathsig closes that hole: the kernel sends SIGKILL to the child when its
// parent dies. The known caveat is that the signal fires when the *thread* that
// started the child exits rather than the whole process, which Go's runtime
// does not promise never to do; the cost if it happens is one stream restart,
// because the supervisor sees the child exit and starts it again, so the
// failure mode is a hiccup instead of a dead site.
func applyProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
