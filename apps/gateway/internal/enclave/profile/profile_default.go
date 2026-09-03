//go:build !nitro_enclave

package profile

// Enabled is a compile-time property. A host-controlled environment variable
// must never be able to turn the measured enclave policy on or off.
const Enabled = false
