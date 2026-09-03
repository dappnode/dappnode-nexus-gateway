#!/usr/bin/env bash

# Manage the enclave measurements allowed to decrypt the Gateway secret bundle.
#
# The key policy gates kms:Decrypt on kms:RecipientAttestation:ImageSha384
# (PCR0) from TWO statements that must always agree:
#
#   DenyParentDecryptUnlessMeasured  Deny  when PCR0 is NOT in the list
#   AllowMeasuredGatewayDecrypt      Allow when PCR0 IS     in the list
#
# Updating only the Allow leaves the Deny blocking the new release, so the
# enclave launches and then cannot decrypt its secrets. Every write here
# rewrites both statements from a single list, then re-reads the result to
# prove they still agree.

set -euo pipefail

DENY_SID=DenyParentDecryptUnlessMeasured
ALLOW_SID=AllowMeasuredGatewayDecrypt
CONDITION_KEY='kms:RecipientAttestation:ImageSha384'

usage() {
  cat >&2 <<'USAGE'
Usage:
  kms-allowlist.sh list
  kms-allowlist.sh add PCR0 [--dry-run]
  kms-allowlist.sh prune --keep PCR0 [--keep PCR0 ...] [--dry-run]

Environment:
  KMS_KEY_ARN   full ARN or key id of the secret-release key (required)
  AWS_REGION    region of that key (required)

add is additive and idempotent, so it is safe on every release: an enclave
whose measurement is absent simply cannot decrypt, it gains nothing.

prune is destructive. It refuses to drop the measurement of the enclave that
is currently running unless that measurement is passed with --keep.
USAGE
  exit 2
}

log() { printf '%s\n' "$*" >&2; }

fail() {
  printf '%s\n' "$*" >&2
  exit 1
}

require_environment() {
  [[ -n "${KMS_KEY_ARN:-}" ]] || fail 'KMS_KEY_ARN is required'
  [[ -n "${AWS_REGION:-}" ]] || fail 'AWS_REGION is required'
}

validate_measurement() {
  local value=$1
  [[ "$value" =~ ^[0-9a-fA-F]{96}$ ]] ||
    fail "PCR0 must be 96 hexadecimal characters, got: $value"
}

normalise_measurements() {
  tr 'A-F' 'a-f' | sed '/^$/d' | sort -u
}

get_policy() {
  aws kms get-key-policy \
    --key-id "$KMS_KEY_ARN" \
    --policy-name default \
    --region "$AWS_REGION" \
    --output text \
    --query Policy
}

put_policy() {
  aws kms put-key-policy \
    --key-id "$KMS_KEY_ARN" \
    --policy-name default \
    --region "$AWS_REGION" \
    --policy "$1"
}

# read_allowlist POLICY_JSON SID -> one lowercase measurement per line.
# A single string and a list are both accepted, because the policy was written
# by hand with a single string before this script existed.
read_allowlist() {
  jq -r --arg sid "$2" --arg key "$CONDITION_KEY" '
    [ .Statement[] | select(.Sid == $sid) ] as $matched
    | if ($matched | length) != 1 then
        error("expected exactly one statement with Sid " + $sid)
      else
        $matched[0].Condition
        | to_entries
        | if length != 1 then error("expected exactly one condition operator") else . end
        | .[0].value[$key]
        | if . == null then error("statement has no " + $key + " condition")
          elif type == "array" then .[]
          else . end
      end
  ' <<<"$1" | normalise_measurements
}

# set_allowlist POLICY_JSON MEASUREMENT... -> new policy JSON
#
# Both statements are rewritten from the same list. That is what makes them
# impossible to diverge, rather than a check applied afterwards.
set_allowlist() {
  local policy=$1
  shift
  local values
  values=$(printf '%s\n' "$@" | normalise_measurements | jq -R . | jq -s .)

  [[ "$(jq 'length' <<<"$values")" -gt 0 ]] ||
    fail 'refusing to write an empty allowlist: no enclave could decrypt'

  jq \
    --arg denySid "$DENY_SID" \
    --arg allowSid "$ALLOW_SID" \
    --arg key "$CONDITION_KEY" \
    --argjson values "$values" '
    [ .Statement[].Sid ] as $sids
    | if ($sids | index($denySid)) == null or ($sids | index($allowSid)) == null then
        error("policy is missing the deny or allow statement")
      else . end
    | .Statement |= map(
        if .Sid == $denySid then
          .Condition = { StringNotEqualsIgnoreCase: { ($key): $values } }
        elif .Sid == $allowSid then
          .Condition = { StringEqualsIgnoreCase: { ($key): $values } }
        else . end
      )
  ' <<<"$policy"
}

# Refuse to proceed unless both statements carry exactly the same measurements.
assert_statements_agree() {
  local policy=$1
  local deny allow
  deny=$(read_allowlist "$policy" "$DENY_SID")
  allow=$(read_allowlist "$policy" "$ALLOW_SID")
  if [[ "$deny" != "$allow" ]]; then
    log 'deny statement gates on:'
    log "$deny"
    log 'allow statement gates on:'
    log "$allow"
    fail 'deny and allow statements disagree; refusing to continue'
  fi
}

command_list() {
  require_environment
  local policy
  policy=$(get_policy)
  assert_statements_agree "$policy"
  read_allowlist "$policy" "$ALLOW_SID"
}

command_add() {
  require_environment
  local measurement=$1 dry_run=$2
  validate_measurement "$measurement"
  measurement=$(printf '%s' "$measurement" | tr 'A-F' 'a-f')

  local policy current updated
  policy=$(get_policy)
  assert_statements_agree "$policy"
  current=$(read_allowlist "$policy" "$ALLOW_SID")

  if grep -qFx "$measurement" <<<"$current"; then
    log "measurement already allowed, nothing to do: $measurement"
    return 0
  fi

  # shellcheck disable=SC2086
  updated=$(set_allowlist "$policy" $current "$measurement")
  assert_statements_agree "$updated"

  if [[ "$dry_run" == true ]]; then
    printf '%s\n' "$updated"
    return 0
  fi

  put_policy "$updated"

  # Re-read rather than trust the write.
  local verified
  verified=$(get_policy)
  assert_statements_agree "$verified"
  grep -qFx "$measurement" <<<"$(read_allowlist "$verified" "$ALLOW_SID")" ||
    fail 'measurement is still absent after the write'
  log "allowed measurement: $measurement"
}

command_prune() {
  require_environment
  local dry_run=$1
  shift
  local keep=("$@")
  [[ ${#keep[@]} -gt 0 ]] || fail 'prune requires at least one --keep measurement'
  local measurement
  for measurement in "${keep[@]}"; do
    validate_measurement "$measurement"
  done

  # Pruning the running enclave's measurement turns its next restart into an
  # outage: it launches, then cannot decrypt its secrets. This is the failure
  # mode that made the retired cutover scripts dangerous to re-run.
  if command -v nitro-cli >/dev/null 2>&1; then
    local running
    running=$(nitro-cli describe-enclaves 2>/dev/null |
      jq -r '.[]?.Measurements.PCR0 // empty' | normalise_measurements || true)
    if [[ -n "$running" ]] &&
      ! printf '%s\n' "${keep[@]}" | normalise_measurements | grep -qFx "$running"; then
      fail "refusing to prune the measurement of the running enclave: $running"
    fi
  fi

  local policy updated
  policy=$(get_policy)
  assert_statements_agree "$policy"
  updated=$(set_allowlist "$policy" "${keep[@]}")
  assert_statements_agree "$updated"

  if [[ "$dry_run" == true ]]; then
    printf '%s\n' "$updated"
    return 0
  fi

  put_policy "$updated"
  local verified
  verified=$(get_policy)
  assert_statements_agree "$verified"
  log 'allowlist now:'
  read_allowlist "$verified" "$ALLOW_SID" >&2
}

main() {
  [[ $# -ge 1 ]] || usage
  local subcommand=$1
  shift

  local dry_run=false
  local keep=()
  local positional=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --dry-run)
        dry_run=true
        shift
        ;;
      --keep)
        [[ $# -ge 2 ]] || usage
        keep+=("$2")
        shift 2
        ;;
      -h | --help) usage ;;
      *)
        positional+=("$1")
        shift
        ;;
    esac
  done

  case "$subcommand" in
    list) command_list ;;
    add)
      [[ ${#positional[@]} -eq 1 ]] || usage
      command_add "${positional[0]}" "$dry_run"
      ;;
    prune) command_prune "$dry_run" "${keep[@]}" ;;
    *) usage ;;
  esac
}

# Only run when executed, so a test can source the transformations.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi
