//go:build !linux

package main

// startReaper does nothing off Linux; the init only runs in the enclave.
func startReaper(func(pid int) bool) {}
