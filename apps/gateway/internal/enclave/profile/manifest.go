package profile

import (
	"crypto/sha512"
	"encoding/json"
)

const (
	// Port 7000 is used by the Nitro image loader while the enclave boots.
	// Keep application traffic on a distinct port to avoid relying on reuse of
	// that implementation-owned listener.
	BootstrapPort      = 7001
	GatewayPort        = 8080
	ParentCID          = 3
	KMSVsockPort       = 8000
	MeteringVsockPort  = 8444
	RouterVsockPort    = 8445
	SecretBundleSchema = 1
	ManifestSchema     = 4
)

// ProviderEgressRoute is a measured, fail-closed mapping from an HTTPS origin
// to the dedicated parent-side vsock proxy that may reach it. The parent only
// sees TLS ciphertext; hostname verification still happens inside the enclave.
type ProviderEgressRoute struct {
	Name      string `json:"name"`
	Host      string `json:"host"`
	Port      uint16 `json:"port"`
	VsockPort uint32 `json:"vsock_port"`
}

// ProviderEgressRoutes returns every external origin required by the active
// production provider set. Keep this list in sync with the parent proxy units.
func ProviderEgressRoutes() []ProviderEgressRoute {
	return []ProviderEgressRoute{
		{Name: "deepseek", Host: "api.deepseek.com", Port: 443, VsockPort: 8443},
		{Name: "novita", Host: "api.novita.ai", Port: 443, VsockPort: 8446},
		{Name: "tinfoil-inference", Host: "inference.tinfoil.sh", Port: 443, VsockPort: 8447},
		{Name: "tinfoil-github", Host: "github-proxy.tinfoil.sh", Port: 443, VsockPort: 8448},
		{Name: "sigstore-tuf", Host: "tuf-repo-cdn.sigstore.dev", Port: 443, VsockPort: 8449},
		{Name: "tinfoil-kds", Host: "kds-proxy.tinfoil.sh", Port: 443, VsockPort: 8450},
		{Name: "tinfoil-tdx", Host: "tdx-proxy.tinfoil.sh", Port: 443, VsockPort: 8451},
	}
}

// These values are injected by the dedicated enclave Docker build using
// linker flags. They are part of the measured binary, not mutable host env.
var (
	SourceRevision = "unknown"
	AWSRegion      = ""
	KMSKeyARN      = ""
)

type manifest struct {
	SchemaVersion  int            `json:"schema_version"`
	Profile        string         `json:"profile"`
	Workload       string         `json:"workload"`
	SourceRevision string         `json:"source_revision"`
	SecretRelease  secretRelease  `json:"secret_release"`
	Egress         egressProfile  `json:"egress"`
	Ingress        ingressProfile `json:"ingress"`
}

type secretRelease struct {
	Provider     string `json:"provider"`
	AWSRegion    string `json:"aws_region"`
	KMSKeyARN    string `json:"kms_key_arn"`
	BundleSchema int    `json:"bundle_schema"`
}

type egressProfile struct {
	ParentCID         uint32                `json:"parent_cid"`
	KMSVsockPort      uint32                `json:"kms_vsock_port"`
	ProviderRoutes    []ProviderEgressRoute `json:"provider_routes"`
	MeteringVsockPort uint32                `json:"metering_vsock_port"`
	RouterVsockPort   uint32                `json:"router_vsock_port"`
}

type ingressProfile struct {
	GatewayVsockPort uint32 `json:"gateway_vsock_port"`
	TLSInEnclave     bool   `json:"tls_in_enclave"`
	E2EE             e2ee   `json:"e2ee"`
}

type e2ee struct {
	Protocol          string `json:"protocol"`
	Suite             string `json:"suite"`
	Endpoint          string `json:"endpoint"`
	RequestEncrypted  bool   `json:"request_encrypted"`
	ResponseEncrypted bool   `json:"response_encrypted"`
}

// ManifestJSON identifies the enclave build. Its SHA-384 digest is signed as
// Nitro attestation user_data. The ingress claim is limited to the measured
// client-to-Gateway body-encryption boundary; it makes no downstream-provider
// or storage claim.
func ManifestJSON() []byte {
	encoded, err := json.Marshal(manifest{
		SchemaVersion:  ManifestSchema,
		Profile:        "nexus-gateway-v2",
		Workload:       "dappnode-nexus-gateway",
		SourceRevision: SourceRevision,
		SecretRelease: secretRelease{
			Provider:     "aws-kms-nitro-recipient",
			AWSRegion:    AWSRegion,
			KMSKeyARN:    KMSKeyARN,
			BundleSchema: SecretBundleSchema,
		},
		Egress: egressProfile{
			ParentCID:         ParentCID,
			KMSVsockPort:      KMSVsockPort,
			ProviderRoutes:    ProviderEgressRoutes(),
			MeteringVsockPort: MeteringVsockPort,
			RouterVsockPort:   RouterVsockPort,
		},
		Ingress: ingressProfile{
			GatewayVsockPort: GatewayPort,
			TLSInEnclave:     false,
			E2EE: e2ee{
				Protocol:          "ehbp-v1",
				Suite:             "DHKEM-X25519-HKDF-SHA256/HKDF-SHA256/AES-256-GCM",
				Endpoint:          "/v1/confidential/chat/completions",
				RequestEncrypted:  true,
				ResponseEncrypted: true,
			},
		},
	})
	if err != nil {
		panic(err)
	}
	return encoded
}

func ManifestDigest() []byte {
	digest := sha512.Sum384(ManifestJSON())
	return digest[:]
}
