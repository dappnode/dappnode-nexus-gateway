#!/usr/bin/env bash

set -euo pipefail

required_variables=(
  VERSION
  RELEASE_ENVIRONMENT
  SOURCE_REVISION
  SOURCE_REPOSITORY
  SOURCE_REF
  WORKFLOW_RUN_URL
  AWS_REGION
  KMS_KEY_ARN
  NORMAL_IMAGE
  NORMAL_IMAGE_DIGEST
  ARTIFACT_DIR
)

for variable_name in "${required_variables[@]}"; do
  if [[ -z "${!variable_name:-}" ]]; then
    printf 'required environment variable %s is empty\n' "$variable_name" >&2
    exit 1
  fi
done

if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
  printf 'invalid release version: %s\n' "$VERSION" >&2
  exit 1
fi
if [[ ! "$RELEASE_ENVIRONMENT" =~ ^[a-z0-9][a-z0-9-]{0,31}$ ]]; then
  printf 'invalid release environment: %s\n' "$RELEASE_ENVIRONMENT" >&2
  exit 1
fi
if [[ ! "$SOURCE_REVISION" =~ ^[0-9a-f]{40}$ ]]; then
  printf 'source revision must be a full lowercase Git commit SHA\n' >&2
  exit 1
fi
if [[ ! "$AWS_REGION" =~ ^[a-z]{2}(-gov)?-[a-z]+-[0-9]+$ ]]; then
  printf 'invalid AWS region: %s\n' "$AWS_REGION" >&2
  exit 1
fi
if [[ ! "$KMS_KEY_ARN" =~ ^arn:(aws|aws-us-gov|aws-cn):kms:[a-z0-9-]+:[0-9]{12}:key/[0-9a-fA-F-]{36}$ ]]; then
  printf 'KMS_KEY_ARN must be a full customer-managed KMS key ARN\n' >&2
  exit 1
fi
if [[ ! "$NORMAL_IMAGE_DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  printf 'invalid normal image digest: %s\n' "$NORMAL_IMAGE_DIGEST" >&2
  exit 1
fi
if [[ "$(uname -m)" != "x86_64" ]]; then
  printf 'the release EIF currently requires an x86_64 build runner\n' >&2
  exit 1
fi

# Reproducible builds need a fixed timestamp. Docker stamps each file's mtime
# and the image creation time with the moment of the build, and that alone
# changed PCR0 and PCR2 on every rebuild of identical source. The commit time is
# used because anyone can derive it from the public repository with
# `git log -1 --format=%ct <revision>`, so reproducing a release needs no value
# published by this build.
if [[ -z "${SOURCE_DATE_EPOCH:-}" ]]; then
  SOURCE_DATE_EPOCH="$(git log -1 --format=%ct "$SOURCE_REVISION" 2>/dev/null || true)"
fi
if [[ ! "$SOURCE_DATE_EPOCH" =~ ^[0-9]+$ ]]; then
  printf 'SOURCE_DATE_EPOCH must be the commit time of %s as a Unix timestamp\n' "$SOURCE_REVISION" >&2
  exit 1
fi
export SOURCE_DATE_EPOCH

nitro_cli_version="1.4.5"
nitro_cli_image="nexus-nitro-cli:${nitro_cli_version}"
enclave_image="nexus-gateway-enclave:${SOURCE_REVISION}"
asset_prefix="nexus-gateway-${VERSION}-${RELEASE_ENVIRONMENT}-${AWS_REGION}-x86_64"
eif_name="${asset_prefix}.eif"
checksum_name="${eif_name}.sha256"
measurements_name="${asset_prefix}.measurements.json"
manifest_name="${asset_prefix}.release.json"
describe_path="$(mktemp)"

mkdir -p "$ARTIFACT_DIR"
if [[ -n "$(find "$ARTIFACT_DIR" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
  printf 'artifact directory must be empty: %s\n' "$ARTIFACT_DIR" >&2
  exit 1
fi
artifact_dir_absolute="$(realpath "$ARTIFACT_DIR")"

# rewrite-timestamp normalises every layer mtime to SOURCE_DATE_EPOCH, and
# --provenance=false keeps a build-time attestation (which embeds the clock and
# the runner identity) out of the image. Without both, two builds of identical
# source produce different layers and therefore different measurements.
#
# The docker driver accepts rewrite-timestamp and ignores it, normalising only
# the config's created field, so it must not be used to cut a release.
# awk must not exit early here: SIGPIPE on docker would abort the script.
builder_driver="$(docker buildx inspect --bootstrap 2>/dev/null | awk -F': *' '/^Driver:/{if (driver == "") driver = $2} END {print driver}')" || builder_driver=""
if [[ "$builder_driver" != "docker-container" ]]; then
  printf 'reproducible build check failed: buildx driver is %s, need docker-container\n' \
    "${builder_driver:-unknown}" >&2
  printf 'The docker driver silently ignores rewrite-timestamp, so this build would\n' >&2
  printf 'produce measurements nobody else can reproduce. Create a builder with:\n' >&2
  printf '  docker buildx create --driver docker-container --use\n' >&2
  exit 1
fi

build_enclave_image() {
  docker buildx build --pull --no-cache --provenance=false \
    --label "org.opencontainers.image.source=https://github.com/${SOURCE_REPOSITORY}" \
    --label "org.opencontainers.image.revision=${SOURCE_REVISION}" \
    --label "org.opencontainers.image.version=${VERSION}" \
    --build-arg "SOURCE_REVISION=${SOURCE_REVISION}" \
    --build-arg "AWS_REGION=${AWS_REGION}" \
    --build-arg "KMS_KEY_ARN=${KMS_KEY_ARN}" \
    -f apps/gateway/Dockerfile.enclave \
    --output "type=docker,name=$1,rewrite-timestamp=true" \
    .
}

build_enclave_image "$enclave_image"

image_created_epoch="$(date -u -d "$(docker image inspect --format '{{.Created}}' "$enclave_image")" +%s)"
if [[ "$image_created_epoch" != "$SOURCE_DATE_EPOCH" ]]; then
  printf 'reproducible build check failed: image timestamp is %s, expected %s\n' \
    "$image_created_epoch" "$SOURCE_DATE_EPOCH" >&2
  printf 'SOURCE_DATE_EPOCH or rewrite-timestamp did not take effect; this build\n' >&2
  printf 'would produce measurements nobody else can reproduce.\n' >&2
  exit 1
fi

# A normalised created field is not proof the layers were normalised, so test
# determinism by building twice instead of asserting it in the manifest.
determinism_image="${enclave_image}-determinism-check"
build_enclave_image "$determinism_image"
first_image_id="$(docker image inspect --format '{{.Id}}' "$enclave_image")"
second_image_id="$(docker image inspect --format '{{.Id}}' "$determinism_image")"
docker image rm -f "$determinism_image" >/dev/null 2>&1 || true
if [[ "$first_image_id" != "$second_image_id" ]]; then
  printf 'reproducible build check failed: two builds of %s produced different images\n' \
    "$SOURCE_REVISION" >&2
  printf '  first:  %s\n' "$first_image_id" >&2
  printf '  second: %s\n' "$second_image_id" >&2
  printf 'Publishing these measurements would break deploy/nitro/verify-build.sh.\n' >&2
  exit 1
fi

# --load is required: a docker-container builder leaves results in the build
# cache, and build-enclave below needs the image in the daemon.
docker buildx build --pull --load \
  -f deploy/nitro/nitro-cli-builder.Dockerfile \
  -t "$nitro_cli_image" \
  deploy/nitro

nitro_run=(
  docker run --rm
  -v /var/run/docker.sock:/var/run/docker.sock
  -v "${artifact_dir_absolute}:/artifacts"
)
"${nitro_run[@]}" "$nitro_cli_image" \
  build-enclave \
  --docker-uri "$enclave_image" \
  --output-file "/artifacts/${eif_name}" \
  --name nexus-gateway \
  --version "$VERSION"

"${nitro_run[@]}" "$nitro_cli_image" \
  describe-eif \
  --eif-path "/artifacts/${eif_name}" \
  > "$describe_path"

jq -e '
  (.CheckCRC == true) and
  (.Measurements.PCR0 | test("^[0-9a-f]{96}$")) and
  (.Measurements.PCR1 | test("^[0-9a-f]{96}$")) and
  (.Measurements.PCR2 | test("^[0-9a-f]{96}$")) and
  (.IsSigned == false) and
  ((.Measurements.PCR8 // null) == null)
' "$describe_path" >/dev/null

(
  cd "$ARTIFACT_DIR"
  sha256sum "$eif_name" > "$checksum_name"
)

jq '{
  schema_version: 2,
  hash_algorithm: "sha384",
  pcr0: .Measurements.PCR0,
  pcr1: .Measurements.PCR1,
  pcr2: .Measurements.PCR2
}' "$describe_path" > "${ARTIFACT_DIR}/${measurements_name}"

eif_sha256="$(cut -d ' ' -f 1 "${ARTIFACT_DIR}/${checksum_name}")"
eif_size="$(stat -c '%s' "${ARTIFACT_DIR}/${eif_name}")"
enclave_image_id="$(docker image inspect --format '{{.Id}}' "$enclave_image")"
nitro_builder_image_id="$(docker image inspect --format '{{.Id}}' "$nitro_cli_image")"

jq -n \
  --arg version "$VERSION" \
  --arg release_environment "$RELEASE_ENVIRONMENT" \
  --arg source_repository "https://github.com/${SOURCE_REPOSITORY}" \
  --arg source_revision "$SOURCE_REVISION" \
  --arg source_ref "$SOURCE_REF" \
  --arg workflow_run_url "$WORKFLOW_RUN_URL" \
  --arg architecture "x86_64" \
  --arg aws_region "$AWS_REGION" \
  --arg kms_key_arn "$KMS_KEY_ARN" \
  --arg normal_image "$NORMAL_IMAGE" \
  --arg normal_image_digest "$NORMAL_IMAGE_DIGEST" \
  --arg enclave_dockerfile "apps/gateway/Dockerfile.enclave" \
  --arg enclave_image_id "$enclave_image_id" \
  --arg nitro_cli_version "$nitro_cli_version" \
  --arg nitro_builder_image_id "$nitro_builder_image_id" \
  --arg eif_filename "$eif_name" \
  --arg eif_sha256 "$eif_sha256" \
  --argjson eif_size "$eif_size" \
  --arg source_date_epoch "$SOURCE_DATE_EPOCH" \
  --slurpfile measurements "${ARTIFACT_DIR}/${measurements_name}" \
  '{
    schema_version: 2,
    release: {
      version: $version,
      environment: $release_environment
    },
    source: {
      repository: $source_repository,
      revision: $source_revision,
      ref: $source_ref
    },
    build: {
      workflow_run_url: $workflow_run_url,
      architecture: $architecture,
      enclave_dockerfile: $enclave_dockerfile,
      enclave_image_id: $enclave_image_id,
      nitro_cli_version: $nitro_cli_version,
      nitro_builder_image_id: $nitro_builder_image_id,
      source_date_epoch: ($source_date_epoch | tonumber),
      aws_region: $aws_region,
      kms_key_arn: $kms_key_arn
    },
    normal_image: {
      reference: $normal_image,
      digest: $normal_image_digest
    },
    enclave: {
      filename: $eif_filename,
      sha256: $eif_sha256,
      size_bytes: $eif_size,
      measurements: $measurements[0]
    },
    reproducibility: {
      measurements: true,
      eif_sha256: false,
      verify_with: "deploy/nitro/verify-build.sh",
      note: "Rebuilding this revision reproduces pcr0/pcr1/pcr2 exactly. The EIF file hash is deliberately not reproducible: nitro-cli stamps the build time into an EIF metadata section that PCR0 excludes by design, so the checksum below verifies a download and must not be used to compare independent builds."
    },
    security_profile: {
      tls_in_enclave: false,
      api_key_in_authorization_header: true,
      body_e2ee: {
        protocol: "ehbp-v1",
        suite: "DHKEM-X25519-HKDF-SHA256/HKDF-SHA256/AES-256-GCM",
        endpoint: "/v1/confidential/chat/completions",
        hpke_key_source: "nitro-attestation-public-key"
      }
    }
  }' > "${ARTIFACT_DIR}/${manifest_name}"

jq -e \
  --arg revision "$SOURCE_REVISION" \
  --arg digest "$NORMAL_IMAGE_DIGEST" \
  --arg eif_sha256 "$eif_sha256" \
  '.source.revision == $revision and
   .normal_image.digest == $digest and
   .enclave.sha256 == $eif_sha256' \
  "${ARTIFACT_DIR}/${manifest_name}" >/dev/null

if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  {
    printf 'artifact_dir=%s\n' "$ARTIFACT_DIR"
    printf 'asset_prefix=%s\n' "$asset_prefix"
    printf 'eif_path=%s\n' "${ARTIFACT_DIR}/${eif_name}"
    printf 'measurements_path=%s\n' "${ARTIFACT_DIR}/${measurements_name}"
    printf 'manifest_path=%s\n' "${ARTIFACT_DIR}/${manifest_name}"
    printf 'manifest_name=%s\n' "$manifest_name"
    printf 'pcr0=%s\n' "$(jq -r '.Measurements.PCR0' "$describe_path")"
    printf 'pcr1=%s\n' "$(jq -r '.Measurements.PCR1' "$describe_path")"
    printf 'pcr2=%s\n' "$(jq -r '.Measurements.PCR2' "$describe_path")"
  } >> "$GITHUB_OUTPUT"
fi

printf 'Created release assets in %s\n' "$ARTIFACT_DIR"
