//go:build nitro_enclave

package profile

// Enabled is baked into the dedicated enclave binary and therefore covered by
// the EIF measurements in its Nitro attestation document.
const Enabled = true
