//go:build linux

package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// startReaper collects processes orphaned onto the init, such as a worker
// whose parent was killed, so they do not stay as zombies. It only reaps
// processes the init did not start itself: those are left for exec.Cmd.Wait,
// which needs their exit status. WNOWAIT peeks at an exited child without
// consuming it, so the two never race.
func startReaper(managed func(pid int) bool) {
	// Outside PID 1 (the Nitro init may run us as its child), ask the kernel
	// to hand orphans to us anyway.
	if os.Getpid() != 1 {
		if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
			log.Printf("cannot become child subreaper: %v", err)
		}
	}
	sigchld := make(chan os.Signal, 16)
	signal.Notify(sigchld, syscall.SIGCHLD)
	// A sweep every few seconds catches an orphan that exited while a
	// managed child was still waiting to be collected.
	tick := time.NewTicker(3 * time.Second)
	go func() {
		for {
			select {
			case <-sigchld:
			case <-tick.C:
			}
			for {
				var info unix.Siginfo
				err := unix.Waitid(unix.P_ALL, 0, &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil)
				pid := siginfoPid(&info)
				if err != nil || pid == 0 {
					break
				}
				if managed(pid) {
					// Its supervisor collects it. Stop here: Waitid with
					// WNOWAIT keeps returning the same child until then.
					break
				}
				var status unix.WaitStatus
				_, _ = unix.Wait4(pid, &status, unix.WNOHANG, nil)
			}
		}
	}()
}

// siginfoPid reads si_pid, which follows si_signo, si_errno, si_code and
// padding at byte 16 of siginfo_t on 64-bit Linux.
func siginfoPid(info *unix.Siginfo) int {
	return int(*(*int32)(unsafe.Pointer(uintptr(unsafe.Pointer(info)) + 16)))
}
