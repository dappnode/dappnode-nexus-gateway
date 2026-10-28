package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/services"
)

// loadSelfHealing reads the provider circuit breaker and retry settings:
//
//	PROVIDER_BREAKER_FAILURES          failures in a row that open a circuit (default 5; 0 turns it off)
//	PROVIDER_BREAKER_OPEN_SECONDS      first open period (default 30)
//	PROVIDER_BREAKER_MAX_OPEN_SECONDS  longest open period after failed probes (default 300)
//	PROVIDER_RETRY_FAST_FAILURES       retry a quick failure once before any output (default true)
func loadSelfHealing() (services.SelfHealing, error) {
	cfg := services.DefaultSelfHealing
	if v := os.Getenv("PROVIDER_BREAKER_FAILURES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return cfg, fmt.Errorf("PROVIDER_BREAKER_FAILURES must be a whole number, 0 or more")
		}
		cfg.BreakerFailures = n
	}
	var err error
	if cfg.BreakerOpenFor, err = timeoutSeconds("PROVIDER_BREAKER_OPEN_SECONDS", cfg.BreakerOpenFor); err != nil {
		return cfg, err
	}
	if cfg.BreakerMaxOpen, err = timeoutSeconds("PROVIDER_BREAKER_MAX_OPEN_SECONDS", cfg.BreakerMaxOpen); err != nil {
		return cfg, err
	}
	if cfg.BreakerMaxOpen < cfg.BreakerOpenFor {
		return cfg, fmt.Errorf("PROVIDER_BREAKER_MAX_OPEN_SECONDS must not be shorter than PROVIDER_BREAKER_OPEN_SECONDS")
	}
	if v := os.Getenv("PROVIDER_RETRY_FAST_FAILURES"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return cfg, fmt.Errorf("PROVIDER_RETRY_FAST_FAILURES must be true or false")
		}
		cfg.RetryFastFailures = b
	}
	return cfg, nil
}
