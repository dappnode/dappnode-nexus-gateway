#!/usr/bin/env bash

# Tests for the pure policy transformations in kms-allowlist.sh. No AWS calls:
# get_policy and put_policy are the only things that touch the network, and
# nothing here uses them.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
source "$script_dir/kms-allowlist.sh"

failures=0
check() {
  local name=$1 actual=$2 expected=$3
  if [[ "$actual" == "$expected" ]]; then
    printf 'ok   %s\n' "$name"
  else
    printf 'FAIL %s\n     got:  %s\n     want: %s\n' "$name" "$actual" "$expected"
    failures=$((failures + 1))
  fi
}

check_fails() {
  local name=$1
  shift
  # A subshell, because these helpers exit on failure by design and would
  # otherwise take the test run down with them.
  if ( "$@" ) >/dev/null 2>&1; then
    printf 'FAIL %s (expected a non-zero exit)\n' "$name"
    failures=$((failures + 1))
  else
    printf 'ok   %s\n' "$name"
  fi
}

pcr_a=$(printf 'a%.0s' {1..96})
pcr_b=$(printf 'b%.0s' {1..96})
pcr_c=$(printf 'c%.0s' {1..96})

# The policy as it exists today: one measurement, written as a bare string.
single_string_policy=$(
  jq -n --arg key "$CONDITION_KEY" --arg pcr "$pcr_a" '{
    Version: "2012-10-17",
    Id: "nexus-gateway-tee-v1",
    Statement: [
      { Sid: "MarcKeyAdministration", Effect: "Allow", Action: ["kms:PutKeyPolicy"] },
      { Sid: "DenyParentDecryptUnlessMeasured", Effect: "Deny", Action: "kms:Decrypt",
        Condition: { StringNotEqualsIgnoreCase: { ($key): $pcr } } },
      { Sid: "AllowMeasuredGatewayDecrypt", Effect: "Allow", Action: "kms:Decrypt",
        Condition: { StringEqualsIgnoreCase: { ($key): $pcr } } }
    ]
  }'
)

# --- reading -------------------------------------------------------------

check "reads a bare string measurement" \
  "$(read_allowlist "$single_string_policy" "$ALLOW_SID")" "$pcr_a"

two=$(set_allowlist "$single_string_policy" "$pcr_a" "$pcr_b")
check "reads a list measurement" \
  "$(read_allowlist "$two" "$ALLOW_SID")" "$(printf '%s\n%s' "$pcr_a" "$pcr_b")"

# --- the load-bearing property -------------------------------------------
#
# Both statements are rewritten from one list, so they cannot diverge. If they
# ever did, the deny would silently block a release the allow permits.

check "add keeps deny and allow identical" \
  "$(read_allowlist "$two" "$DENY_SID")" "$(read_allowlist "$two" "$ALLOW_SID")"

three=$(set_allowlist "$two" "$pcr_a" "$pcr_b" "$pcr_c")
check "three releases stay identical across statements" \
  "$(read_allowlist "$three" "$DENY_SID")" "$(read_allowlist "$three" "$ALLOW_SID")"

check "assert_statements_agree accepts a consistent policy" \
  "$(assert_statements_agree "$three" && echo consistent)" "consistent"

# A policy where only the allow was updated is exactly the mistake this guards
# against, so it must be rejected rather than silently repaired.
divergent=$(
  jq --arg allowSid "$ALLOW_SID" --arg key "$CONDITION_KEY" --arg pcr "$pcr_b" '
    .Statement |= map(
      if .Sid == $allowSid then
        .Condition = { StringEqualsIgnoreCase: { ($key): [$pcr] } }
      else . end)
  ' <<<"$single_string_policy"
)
check_fails "divergent statements are rejected" assert_statements_agree "$divergent"

# --- refusals ------------------------------------------------------------

check_fails "empty allowlist is refused" set_allowlist "$single_string_policy"

missing_statement=$(
  jq --arg denySid "$DENY_SID" '.Statement |= map(select(.Sid != $denySid))' \
    <<<"$single_string_policy"
)
check_fails "policy missing the deny statement is refused" \
  set_allowlist "$missing_statement" "$pcr_a"

check_fails "short measurement is refused" validate_measurement "abc123"
check_fails "non-hex measurement is refused" \
  validate_measurement "$(printf 'z%.0s' {1..96})"
check "96 hex characters are accepted" \
  "$(validate_measurement "$pcr_a" && echo valid)" "valid"

# --- idempotence and normalisation ---------------------------------------

check "adding the same measurement twice changes nothing" \
  "$(read_allowlist "$(set_allowlist "$two" "$pcr_a" "$pcr_b" "$pcr_b")" "$ALLOW_SID")" \
  "$(read_allowlist "$two" "$ALLOW_SID")"

upper=$(set_allowlist "$single_string_policy" "$(printf 'A%.0s' {1..96})")
check "uppercase input is normalised to lowercase" \
  "$(read_allowlist "$upper" "$ALLOW_SID")" "$pcr_a"

# Unrelated statements must survive untouched, or a rewrite could silently
# drop key administration and lock the account out of its own key.
check "unrelated statements are preserved" \
  "$(jq -r '[.Statement[].Sid] | join(",")' <<<"$three")" \
  "MarcKeyAdministration,DenyParentDecryptUnlessMeasured,AllowMeasuredGatewayDecrypt"

check "policy metadata is preserved" \
  "$(jq -r '.Id + " " + .Version' <<<"$three")" "nexus-gateway-tee-v1 2012-10-17"

# --- command-level behaviour, with AWS and nitro-cli stubbed ---------------
#
# get_policy/put_policy are the only functions that touch the network, so
# redefining them here exercises the real command logic offline.

export KMS_KEY_ARN=test-key AWS_REGION=eu-central-1
stub_state=""

get_policy() { printf '%s\n' "$stub_state"; }
put_policy() { stub_state=$1; }

stub_state=$single_string_policy
command_add "$pcr_b" false >/dev/null 2>&1
check "add writes the new measurement through" \
  "$(read_allowlist "$stub_state" "$ALLOW_SID")" "$(printf '%s\n%s' "$pcr_a" "$pcr_b")"
check "add leaves both statements in agreement" \
  "$(read_allowlist "$stub_state" "$DENY_SID")" "$(read_allowlist "$stub_state" "$ALLOW_SID")"

before=$stub_state
command_add "$pcr_a" false >/dev/null 2>&1
check "re-adding an existing measurement writes nothing" "$stub_state" "$before"

before=$stub_state
command_add "$pcr_c" true >/dev/null 2>&1
check "--dry-run writes nothing" "$stub_state" "$before"

# The guard that matters: pruning away the measurement of the enclave that is
# currently running makes its next restart an outage, because it launches and
# then cannot decrypt. Stub nitro-cli to report a running enclave.
stub_bin=$(mktemp -d)
cat >"$stub_bin/nitro-cli" <<STUB
#!/usr/bin/env bash
printf '[{"Measurements":{"PCR0":"%s"}}]\n' "$pcr_a"
STUB
chmod +x "$stub_bin/nitro-cli"
PATH="$stub_bin:$PATH"

check_fails "prune refuses to drop the running enclave's measurement" \
  command_prune false "$pcr_b"

before=$stub_state
check "prune keeping the running measurement is allowed" \
  "$(command_prune true "$pcr_a" >/dev/null 2>&1 && echo allowed)" "allowed"
check "that prune was a dry run and wrote nothing" "$stub_state" "$before"

rm -rf "$stub_bin"

if [[ $failures -gt 0 ]]; then
  printf '\n%d test(s) failed\n' "$failures"
  exit 1
fi
printf '\nall tests passed\n'
