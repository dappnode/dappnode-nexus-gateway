package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/dappnode/dappnode-nexus-gateway/internal/enclavebundle"
	"github.com/mdlayher/vsock"
)

const (
	maxBootstrapBytes         = 256 * 1024
	maxEnvelopeBytes          = 192 * 1024
	maxBootstrapResponseBytes = 4 * 1024
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "ingress":
		runIngress(os.Args[2:])
	case "bootstrap":
		runBootstrap(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: nexus-vsock-forwarder <ingress|bootstrap> [flags]")
	os.Exit(2)
}

func runIngress(arguments []string) {
	flags := flag.NewFlagSet("ingress", flag.ExitOnError)
	listenAddress := flags.String("listen", "127.0.0.1:18080", "host TCP address exposed to the reverse proxy")
	cid := flags.Uint("cid", 16, "enclave CID")
	port := flags.Uint("port", 8080, "enclave vsock HTTP port")
	maxConnections := flags.Int("max-connections", 256, "maximum concurrent forwarded connections")
	flags.Parse(arguments)
	validateVsockAddress(*cid, *port)
	if *maxConnections < 1 || *maxConnections > 4096 {
		log.Fatal("max-connections must be between 1 and 4096")
	}

	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		log.Fatalf("listen on %s: %v", *listenAddress, err)
	}
	defer listener.Close()
	log.Printf("forwarding %s to vsock %d:%d", listener.Addr(), *cid, *port)

	limit := make(chan struct{}, *maxConnections)
	for {
		connection, err := listener.Accept()
		if err != nil {
			log.Fatalf("accept ingress connection: %v", err)
		}
		select {
		case limit <- struct{}{}:
			go func() {
				defer func() { <-limit }()
				forward(connection, uint32(*cid), uint32(*port))
			}()
		default:
			_ = connection.Close()
		}
	}
}

func forward(source net.Conn, cid, port uint32) {
	defer source.Close()
	destination, err := vsock.Dial(cid, port, nil)
	if err != nil {
		log.Printf("dial enclave vsock %d:%d: %v", cid, port, err)
		return
	}
	defer destination.Close()

	done := make(chan struct{}, 2)
	go copyHalf(destination, source, done)
	go copyHalf(source, destination, done)
	<-done
	<-done
}

func copyHalf(destination io.Writer, source io.Reader, done chan<- struct{}) {
	_, _ = io.Copy(destination, source)
	if closer, ok := destination.(interface{ CloseWrite() error }); ok {
		_ = closer.CloseWrite()
	}
	done <- struct{}{}
}

func runBootstrap(arguments []string) {
	flags := flag.NewFlagSet("bootstrap", flag.ExitOnError)
	cid := flags.Uint("cid", 16, "enclave CID")
	port := flags.Uint("port", 7001, "enclave bootstrap vsock port")
	envelopePath := flags.String("envelope", "", "encrypted enclave envelope JSON")
	flags.Parse(arguments)
	validateVsockAddress(*cid, *port)
	if flags.NArg() != 0 || *envelopePath == "" {
		log.Fatal("bootstrap requires --envelope")
	}

	payload := readBootstrapPayload(*envelopePath)
	defer enclavebundle.Clear(payload)

	connection, err := vsock.Dial(uint32(*cid), uint32(*port), nil)
	if err != nil {
		log.Fatalf("dial enclave bootstrap: %v", err)
	}
	defer connection.Close()
	if _, err := connection.Write(payload); err != nil {
		log.Fatalf("send enclave bootstrap: %v", err)
	}
	if err := connection.CloseWrite(); err != nil {
		log.Fatalf("finish enclave bootstrap request: %v", err)
	}
	response, err := io.ReadAll(io.LimitReader(connection, maxBootstrapResponseBytes+1))
	if err != nil {
		log.Fatalf("read enclave bootstrap response: %v", err)
	}
	if len(response) > maxBootstrapResponseBytes {
		log.Fatal("enclave bootstrap response is too large")
	}
	if string(response) != "OK\n" {
		diagnostic := bytes.TrimSpace(response)
		if len(diagnostic) == 0 {
			log.Fatal("enclave rejected bootstrap configuration without a diagnostic")
		}
		log.Fatalf("enclave rejected bootstrap configuration: %q", diagnostic)
	}
	fmt.Print("OK\n")
}

func readBootstrapPayload(envelopePath string) []byte {
	file, err := os.Open(envelopePath)
	if err != nil {
		log.Fatalf("open encrypted envelope: %v", err)
	}
	defer file.Close()
	envelopeBytes, err := io.ReadAll(io.LimitReader(file, maxEnvelopeBytes+1))
	if err != nil {
		log.Fatalf("read encrypted envelope: %v", err)
	}
	if len(envelopeBytes) == 0 || len(envelopeBytes) > maxEnvelopeBytes {
		log.Fatal("encrypted envelope has an invalid size")
	}
	var envelope enclavebundle.Envelope
	if err := decodeStrictJSON(envelopeBytes, &envelope); err != nil {
		log.Fatalf("decode encrypted envelope: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	awsConfig, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("load parent AWS configuration: %v", err)
	}
	credentialsValue, err := awsConfig.Credentials.Retrieve(ctx)
	if err != nil {
		log.Fatalf("retrieve parent role credentials: %v", err)
	}
	if !credentialsValue.CanExpire || credentialsValue.Expires.IsZero() {
		log.Fatal("bootstrap requires temporary parent-role credentials")
	}

	request := enclavebundle.BootstrapRequest{
		SchemaVersion: enclavebundle.BootstrapSchemaVersion,
		Credentials: enclavebundle.AWSCredentials{
			AccessKeyID:     credentialsValue.AccessKeyID,
			SecretAccessKey: credentialsValue.SecretAccessKey,
			SessionToken:    credentialsValue.SessionToken,
			ExpiresAt:       credentialsValue.Expires,
		},
		Envelope: envelope,
	}
	if err := enclavebundle.ValidateBootstrapRequest(request, time.Now().UTC()); err != nil {
		log.Fatalf("validate parent role credentials: %v", err)
	}
	payload, err := json.Marshal(request)
	if err != nil {
		log.Fatal("encode encrypted bootstrap request")
	}
	if len(payload) > maxBootstrapBytes {
		log.Fatal("encrypted bootstrap request is too large")
	}
	return payload
}

func decodeStrictJSON(payload []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateVsockAddress(cid, port uint) {
	if cid < 4 || cid > uint(^uint32(0)) {
		log.Fatal("cid must be between 4 and 4294967295")
	}
	if port < 4 || port > uint(^uint32(0)) {
		log.Fatal("port must be between 4 and 4294967295")
	}
}
