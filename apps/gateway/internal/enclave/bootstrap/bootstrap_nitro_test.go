//go:build nitro_enclave

package bootstrap

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestWriteBootstrapFailureIsBounded(t *testing.T) {
	var output bytes.Buffer
	writeBootstrapFailure(&output, errors.New(strings.Repeat("x", maxBootstrapResponseBytes*2)))

	if output.Len() != maxBootstrapResponseBytes {
		t.Fatalf("response length = %d, want %d", output.Len(), maxBootstrapResponseBytes)
	}
	if !bytes.HasPrefix(output.Bytes(), []byte("ERROR ")) {
		t.Fatal("response does not identify a bootstrap error")
	}
	if !bytes.HasSuffix(output.Bytes(), []byte("\n")) {
		t.Fatal("response is not newline terminated")
	}
}
