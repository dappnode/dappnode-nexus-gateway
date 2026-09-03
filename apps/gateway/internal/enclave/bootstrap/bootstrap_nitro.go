//go:build nitro_enclave

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/enclave/profile"
	"github.com/mdlayher/vsock"
)

const (
	maxBootstrapBytes         = 256 * 1024
	maxBootstrapResponseBytes = 4 * 1024
)

// Load accepts temporary parent-role credentials and an encrypted envelope.
// Plaintext Nexus credentials are released only after AWS KMS validates the
// enclave attestation and re-encrypts the data key to its ephemeral RSA key.
func Load() error {
	listener, err := vsock.Listen(profile.BootstrapPort, nil)
	if err != nil {
		return fmt.Errorf("listen for enclave bootstrap: %w", err)
	}
	defer listener.Close()
	if err := listener.SetDeadline(time.Now().Add(10 * time.Minute)); err != nil {
		return fmt.Errorf("set bootstrap deadline: %w", err)
	}

	connection, err := listener.Accept()
	if err != nil {
		return fmt.Errorf("accept enclave bootstrap: %w", err)
	}
	defer connection.Close()
	if !isHost(connection.RemoteAddr()) {
		return errors.New("reject bootstrap from non-host CID")
	}
	if err := connection.SetDeadline(time.Now().Add(90 * time.Second)); err != nil {
		return fmt.Errorf("set bootstrap connection deadline: %w", err)
	}

	payload, err := io.ReadAll(io.LimitReader(connection, maxBootstrapBytes+1))
	if err != nil {
		return fmt.Errorf("read enclave bootstrap: %w", err)
	}
	if len(payload) == 0 || len(payload) > maxBootstrapBytes {
		return errors.New("enclave bootstrap payload size is invalid")
	}
	defer clear(payload)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := loadPayload(
		ctx,
		payload,
		time.Now().UTC(),
		profile.KMSKeyARN,
		profile.AWSRegion,
		profile.SourceRevision,
		decryptDataKey,
		os.Setenv,
	); err != nil {
		writeBootstrapFailure(connection, err)
		return err
	}
	_, _ = connection.Write([]byte("OK\n"))
	return nil
}

func writeBootstrapFailure(writer io.Writer, err error) {
	message := []byte("ERROR " + err.Error() + "\n")
	if len(message) > maxBootstrapResponseBytes {
		message = append(message[:maxBootstrapResponseBytes-1], '\n')
	}
	_, _ = writer.Write(message)
}

func isHost(address net.Addr) bool {
	vsockAddress, ok := address.(*vsock.Addr)
	return ok && vsockAddress.ContextID == profile.ParentCID
}
