#!/usr/bin/env bash

# Independently rebuild a published Nexus Gateway release and compare the
# measurements against the ones in its signed release manifest.
#
# This needs no AWS account, no credentials, and no Nitro hardware: building an
# EIF is a local operation, and the measurements are a property of the image
# rather than of the machine that built it. Verify the manifest's Cosign
# signature first -- rebuilding against an unauthenticated manifest proves
# nothing, because an attacker who supplies the manifest also supplies the
# expected values.
#
#   cosign verify-blob \
#     --bundle <manifest>.sigstore.json \
#     --certificate-identity https://github.com/dappnode/dappnode-nexus-gateway/.github/workflows/release-and-push.yml@refs/heads/main \
#     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
#     <manifest>
#
#   ./deploy/nitro/verify-build.sh <manifest>.release.json
#
# A PASS means the published measurements really are what this source revision
# compiles to. It does not tell you whether that revision is trustworthy: read
# the code for that.

set -euo pipefail

if [[ $# -ne 1 ]]; then
  printf 'usage: %s <release-manifest.json>\n' "$0" >&2
  exit 2
fi

manifest_path="$1"

for tool in jq git docker; do
  command -v "$tool" >/dev/null || { printf 'missing required tool: %s\n' "$tool" >&2; exit 1; }
done
docker buildx version >/dev/null 2>&1 || {
  printf 'docker buildx is required: the reproducible build depends on rewrite-timestamp\n' >&2
  exit 1
}

# The docker driver ignores rewrite-timestamp and would report a mismatch
# against an honest release, so refuse to run rather than accuse falsely.
# awk must not exit early here: SIGPIPE on docker would abort the script.
verify_builder_driver="$(docker buildx inspect --bootstrap 2>/dev/null | awk -F': *' '/^Driver:/{if (driver == "") driver = $2} END {print driver}')" || verify_builder_driver=""
if [[ "$verify_builder_driver" != "docker-container" ]]; then
  printf 'cannot verify with the %s buildx driver: it ignores rewrite-timestamp\n' \
    "${verify_builder_driver:-unknown}" >&2
  printf 'and would report a mismatch for a correctly built release. Run:\n' >&2
  printf '  docker buildx create --driver docker-container --use\n' >&2
  printf 'and try again.\n' >&2
  exit 1
fi
[[ -f "$manifest_path" ]] || { printf 'no such manifest: %s\n' "$manifest_path" >&2; exit 1; }

# Newline-separated rather than @tsv: tab is an IFS whitespace character, so a
# missing field would collapse and silently shift every later column. jq -r
# renders an absent field as the literal "null", which the checks below reject.
mapfile -t manifest_fields < <(
  jq -r '
    .schema_version, .source.revision, .build.aws_region, .build.kms_key_arn,
    .build.source_date_epoch, .build.nitro_cli_version, .build.architecture,
    .enclave.measurements.pcr0, .enclave.measurements.pcr1, .enclave.measurements.pcr2
  ' "$manifest_path"
)

if [[ "${#manifest_fields[@]}" -ne 10 ]]; then
  printf 'not a Gateway release manifest: %s\n' "$manifest_path" >&2
  exit 1
fi

schema="${manifest_fields[0]}"
revision="${manifest_fields[1]}"
region="${manifest_fields[2]}"
kms_key_arn="${manifest_fields[3]}"
source_date_epoch="${manifest_fields[4]}"
nitro_cli_version="${manifest_fields[5]}"
architecture="${manifest_fields[6]}"
expected_pcr0="${manifest_fields[7]}"
expected_pcr1="${manifest_fields[8]}"
expected_pcr2="${manifest_fields[9]}"

if [[ ! "$schema" =~ ^[0-9]+$ ]]; then
  printf 'manifest has no usable schema_version: %s\n' "$manifest_path" >&2
  exit 1
fi
if [[ "$schema" -lt 2 ]]; then
  printf 'manifest schema %s predates reproducible builds.\n' "$schema" >&2
  printf 'Releases built before that change cannot be reproduced: the build did not pin\n' >&2
  printf 'a timestamp, so every rebuild produced different measurements by construction.\n' >&2
  exit 1
fi

for pair in "revision:$revision" "aws_region:$region" "kms_key_arn:$kms_key_arn" \
            "source_date_epoch:$source_date_epoch" "architecture:$architecture" \
            "pcr0:$expected_pcr0" "pcr1:$expected_pcr1" "pcr2:$expected_pcr2"; do
  [[ -n "${pair#*:}" && "${pair#*:}" != "null" ]] || {
    printf 'manifest is missing %s\n' "${pair%%:*}" >&2; exit 1; }
done

if [[ ! "$source_date_epoch" =~ ^[0-9]+$ ]]; then
  printf 'manifest source_date_epoch is not a Unix timestamp: %s\n' "$source_date_epoch" >&2
  exit 1
fi

if [[ "$(uname -m)" != "$architecture" ]]; then
  printf 'this release was built for %s, but this machine is %s\n' "$architecture" "$(uname -m)" >&2
  printf 'measurements are architecture-specific and will not match.\n' >&2
  exit 1
fi

git cat-file -e "${revision}^{commit}" 2>/dev/null || {
  printf 'revision %s is not in this repository; fetch it first\n' "$revision" >&2; exit 1; }

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
context="$workdir/src"
artifacts="$workdir/artifacts"
mkdir -p "$context" "$artifacts"

# Build from the committed tree, never the working directory, so uncommitted or
# untracked files cannot change the result.
git archive "$revision" | tar -x -C "$context"

enclave_image="nexus-gateway-enclave-verify:${revision}"
nitro_cli_image="nexus-nitro-cli-verify:${nitro_cli_version}"

build_log="$workdir/build.log"
# Builds are quiet unless something fails: the interesting output is the
# measurement comparison, not the layer log.
fail() {
  printf '\nbuild failed; last lines of the build log:\n' >&2
  tail -30 "$build_log" >&2
  exit 1
}

printf 'Rebuilding %s (source_date_epoch=%s)\nThis takes a few minutes.\n' \
  "${revision:0:12}" "$source_date_epoch"

# --load: a docker-container builder keeps results in the build cache, and
# build-enclave below needs the image in the daemon.
docker buildx build --pull -q --load \
  -f "$context/deploy/nitro/nitro-cli-builder.Dockerfile" \
  -t "$nitro_cli_image" \
  "$context/deploy/nitro" >>"$build_log" 2>&1 || fail

SOURCE_DATE_EPOCH="$source_date_epoch" docker buildx build --pull --no-cache --provenance=false \
  --build-arg "SOURCE_REVISION=${revision}" \
  --build-arg "AWS_REGION=${region}" \
  --build-arg "KMS_KEY_ARN=${kms_key_arn}" \
  -f "$context/apps/gateway/Dockerfile.enclave" \
  --output "type=docker,name=${enclave_image},rewrite-timestamp=true" \
  "$context" >>"$build_log" 2>&1 || fail

# The image labels carry the version and repository. They live in the image
# config rather than the filesystem, so they do not reach the measurements and
# are deliberately not reproduced here.
docker run --rm \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v "${artifacts}:/artifacts" \
  "$nitro_cli_image" build-enclave \
  --docker-uri "$enclave_image" \
  --output-file /artifacts/verify.eif \
  --name nexus-gateway \
  --version "$(jq -r '.release.version' "$manifest_path")" >>"$build_log" 2>&1 || fail

describe="$(docker run --rm -v "${artifacts}:/artifacts" "$nitro_cli_image" \
  describe-eif --eif-path /artifacts/verify.eif 2>>"$build_log")" || fail

status=0
printf '\n%-6s %-8s %s\n' "PCR" "RESULT" "VALUE"
for index in 0 1 2; do
  expected_var="expected_pcr${index}"
  expected="${!expected_var}"
  actual="$(printf '%s' "$describe" | jq -r ".Measurements.PCR${index}")"
  if [[ "$actual" == "$expected" ]]; then
    printf 'PCR%-3s %-8s %s\n' "$index" "match" "$actual"
  else
    status=1
    printf 'PCR%-3s %-8s expected %s\n' "$index" "MISMATCH" "$expected"
    printf '%-6s %-8s rebuilt  %s\n' "" "" "$actual"
  fi
done

docker image rm -f "$enclave_image" >/dev/null 2>&1 || true

echo
if [[ "$status" -eq 0 ]]; then
  printf 'PASS: %s reproduces the published measurements.\n' "${revision:0:12}"
else
  printf 'FAIL: the rebuild does not match the published measurements.\n'
  printf 'Before concluding the release is dishonest, confirm you used the same\n'
  printf 'architecture and an unmodified checkout of %s.\n' "${revision:0:12}"
  printf 'Please report a genuine mismatch: it is exactly what this script is for.\n'
fi
exit "$status"
