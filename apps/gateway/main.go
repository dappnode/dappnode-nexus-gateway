package main

import (
	"context"
	"crypto/rand"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	nsmattestation "github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/attestation/nsm"
	gwhttp "github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/handlers"
	meteringadapter "github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/metering"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/observability/metrics"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/pii/presidio"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/providers/anthropic"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/providers/openai"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/providers/registry"
	tinfoilprovider "github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/providers/tinfoil"
	routerclient "github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/router"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/services"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/enclave/bootstrap"
	enclavenetwork "github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/enclave/network"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/enclave/profile"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/fx"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/observability/logger"
	"github.com/tinfoilsh/encrypted-http-body-protocol/identity"
)

func main() {
	if profile.Enabled {
		if err := profile.ValidateBuild(); err != nil {
			log.Fatalf("invalid enclave build: %v", err)
		}
		if err := bootstrap.Load(); err != nil {
			log.Fatalf("failed to bootstrap enclave: %v", err)
		}
		if err := enclavenetwork.ConfigureDefaultTransport(); err != nil {
			log.Fatalf("failed to configure enclave egress: %v", err)
		}
		tinfoilTLSBridge, err := enclavenetwork.StartTinfoilTLSBridge()
		if err != nil {
			log.Fatalf("failed to configure Tinfoil enclave verification: %v", err)
		}
		defer tinfoilTLSBridge.Close()
	}

	port := envOr("PORT", "8080")
	logLevel := envOr("LOG_LEVEL", "info")
	routerURL := envOr("ROUTER_URL", "http://localhost:8083")
	meteringURL := envOr("METERING_URL", "http://localhost:8084")
	meteringToken := envOr("METERING_TOKEN", "dev-metering-token")
	providerTimeout := 300 * time.Second

	zapLogger, err := logger.NewZapLogger(logLevel)
	if err != nil {
		log.Fatalf("failed to create logger: %v", err)
	}
	defer zapLogger.Sync()

	zapLogger.Info("starting gateway", "port", port)

	ctx := context.Background()
	providerRegistry := registry.NewRegistry()
	providerRegistry.Register("anthropic", anthropic.NewAdapter(providerTimeout))
	providerRegistry.Register("tinfoil", tinfoilprovider.NewAdapter(providerTimeout, zapLogger))
	// Any provider not explicitly registered falls back to the OpenAI-compatible adapter.
	// This allows adding new providers (e.g. novita, mistral) via DB only — no code changes.
	providerRegistry.SetDefault(openai.NewAdapter(providerTimeout, zapLogger))

	meteringClient := meteringadapter.NewClient(meteringURL, meteringToken, 5*time.Second)
	authService := meteringClient
	modelCatalog := meteringClient
	tinfoilProofRepo := meteringClient
	routerClient := routerclient.NewClient(routerURL, 5*time.Second)
	eurToUSDFallbackRate := envFloatOr("EUR_TO_USD_FALLBACK_RATE", 1.08)
	fxProvider := fx.NewFrankfurter(2 * time.Second)

	piiFilter, piiLang, piiFailOpen := buildPIIFilter(zapLogger)

	listModelsSvc := services.NewListModelsServiceWithRateProvider(modelCatalog, zapLogger, fxProvider, eurToUSDFallbackRate)
	generateSvc := services.NewGenerateService(authService, modelCatalog, routerClient, providerRegistry, meteringClient, piiFilter, zapLogger)
	generateSvc.SetPIIOptions(piiLang, piiFailOpen)
	generateSvc.SetTinfoilProofRepository(tinfoilProofRepo)
	chatCompletionsSvc := services.NewChatCompletionsService(generateSvc, zapLogger)

	healthHandler := handlers.NewHealthHandler()
	modelsHandler := handlers.NewModelsHandler(listModelsSvc)
	chatHandler := handlers.NewChatCompletionsHandler(chatCompletionsSvc, zapLogger)
	tinfoilHandler := handlers.NewTinfoilHandler(authService, tinfoilProofRepo, zapLogger)
	var attestationHandler *handlers.AttestationHandler
	var confidentialChatHandler *handlers.ConfidentialChatCompletionsHandler
	if profile.Enabled {
		ehbpIdentity, err := identity.NewIdentity()
		if err != nil {
			log.Fatalf("failed to generate ephemeral EHBP identity: %v", err)
		}
		hpkePublicKey := ehbpIdentity.MarshalPublicKey()
		if len(hpkePublicKey) != 32 {
			log.Fatalf("invalid ephemeral EHBP public key length: %d", len(hpkePublicKey))
		}

		attester, err := nsmattestation.New()
		if err != nil {
			log.Fatalf("failed to initialize Nitro attestation: %v", err)
		}
		defer attester.Close()
		selfTestNonce := make([]byte, 32)
		if _, err := rand.Read(selfTestNonce); err != nil {
			log.Fatalf("failed to generate attestation self-test nonce: %v", err)
		}
		if _, err := attester.Attest(ctx, selfTestNonce, profile.ManifestDigest(), hpkePublicKey); err != nil {
			log.Fatalf("Nitro attestation self-test failed: %v", err)
		}
		attestationHandler = handlers.NewAttestationHandler(attester, hpkePublicKey, zapLogger)
		confidentialChatHandler, err = handlers.NewConfidentialChatCompletionsHandler(
			ehbpIdentity,
			http.HandlerFunc(chatHandler.Handle),
			zapLogger,
		)
		if err != nil {
			log.Fatalf("failed to initialize confidential chat endpoint: %v", err)
		}
	}

	router := gwhttp.NewRouter(healthHandler, modelsHandler, chatHandler, confidentialChatHandler, tinfoilHandler, attestationHandler, zapLogger)

	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", metrics.Handler())

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 300 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		metricsAddr := envOr("METRICS_PORT", "9090")
		zapLogger.Info("metrics server starting", "port", metricsAddr)
		if err := http.ListenAndServe(":"+metricsAddr, metricsMux); err != nil {
			zapLogger.Error("metrics server error", "error", err)
		}
	}()

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		zapLogger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	listenPort, err := strconv.ParseUint(port, 10, 32)
	if err != nil || listenPort == 0 {
		log.Fatalf("invalid gateway port %q", port)
	}
	listener, err := enclavenetwork.ListenGateway(uint32(listenPort))
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	zapLogger.Info("gateway listening", "addr", listener.Addr().String(), "metering_url", meteringURL)
	if err := srv.Serve(listener); err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
	zapLogger.Info("gateway stopped")
}

func envOr(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func envFloatOr(key string, defaultVal float64) float64 {
	if v := os.Getenv(key); v != "" {
		if parsed, err := strconv.ParseFloat(v, 64); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultVal
}

// buildPIIFilter wires the Presidio adapter from environment variables.
// PII_FILTER_ENABLED defaults to true; per-key pii_mode still decides whether
// a request is masked. Setting it to "false" / "0" returns the noop filter so
// masking becomes a no-op even for privacy-enabled keys.
func buildPIIFilter(log *logger.ZapLogger) (ports.PIIFilter, string, bool) {
	enabled := strings.ToLower(envOr("PII_FILTER_ENABLED", "true"))
	if enabled == "false" || enabled == "0" || enabled == "no" {
		log.Info("pii filter disabled")
		return presidio.NewNoopFilter(), "en", false
	}

	url := envOr("PRESIDIO_ANALYZER_URL", "http://presidio-analyzer:3000")
	language := envOr("PII_FILTER_LANGUAGE", "en")
	failOpen := strings.EqualFold(envOr("PII_FILTER_FAIL_OPEN", "false"), "true")

	threshold := 0.4
	if v := os.Getenv("PII_FILTER_SCORE_THRESHOLD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			threshold = f
		}
	}

	timeout := 1500 * time.Millisecond
	if v := os.Getenv("PII_FILTER_TIMEOUT_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			timeout = time.Duration(ms) * time.Millisecond
		}
	}

	log.Info("pii filter enabled",
		"analyzer_url", url,
		"language", language,
		"score_threshold", threshold,
		"timeout_ms", timeout.Milliseconds(),
		"fail_open", failOpen,
	)
	return presidio.NewAdapter(presidio.Config{
		BaseURL:         url,
		DefaultLanguage: language,
		ScoreThreshold:  threshold,
		Timeout:         timeout,
		Logger:          log,
	}), language, failOpen
}
