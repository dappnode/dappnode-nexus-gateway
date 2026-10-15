package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type transportTimeouts struct {
	ProviderSilence time.Duration
	ClientIO        time.Duration
}

func loadTransportTimeouts() (transportTimeouts, error) {
	provider, err := timeoutSeconds("PROVIDER_SILENCE_TIMEOUT_SECONDS", 15*time.Minute)
	if err != nil {
		return transportTimeouts{}, err
	}
	clientIO, err := timeoutSeconds("CLIENT_IO_TIMEOUT_SECONDS", time.Minute)
	return transportTimeouts{ProviderSilence: provider, ClientIO: clientIO}, err
}

func timeoutSeconds(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds <= 0 || seconds > int64((1<<63-1)/time.Second) {
		return 0, fmt.Errorf("%s must be a positive number of seconds", name)
	}
	return time.Duration(seconds) * time.Second, nil
}
