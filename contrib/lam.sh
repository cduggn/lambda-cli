#!/usr/bin/env bash
# lam — a small Lambda Cloud CLI over the REST API (https://cloud.lambda.ai/api/v1).
# Launch, wait for SSH, bootstrap with cloud-init, list, ssh, terminate. No console needed.
#
# Requires: bash 4+ (or macOS bash 3.2), curl, jq, ssh, perl.
# Config: env vars, or ~/.config/lam/config (see config.example). LAMBDA_API_KEY is required.
set -euo pipefail

VERSION="0.1.0"
API="${LAMBDA_API_BASE:-https://cloud.lambda.ai/api/v1}"
CONFIG_FILE="${LAM_CONFIG:-$HOME/.config/lam/config}"
STATE_DIR="${LAM_STATE_DIR:-$HOME/.local/state/lam}"
LAM_HOME="$(cd "$(dirname "$(readlink -f "$0" 2>/dev/null || echo "$0")")" && pwd)"

# shellcheck disable=SC1090
[[ -f "$CONFIG_FILE" ]] && source "$CONFIG_FILE"

: "${LAM_REGION:=us-east-1}"
: "${LAM_TYPE:=gpu_1x_a100_sxm4}"
: "${LAM_SSH_KEY:=inference-eng}"                     # key *name* as registered in Lambda
: "${LAM_SSH_PRIVATE_KEY:=$HOME/.ssh/inference-eng.pem}" # matching private key on this Mac
: "${LAM_SSH_USER:=ubuntu}"
: "${LAM_IMAGE_FAMILY:=lambda-stack-22-04}"
: "${LAM_CLOUD_INIT:=}"                               # default template name or path, "" = none
: "${LAM_ACTIVE_TIMEOUT:=900}"                        # seconds to wait for status=active
: "${LAM_SSH_TIMEOUT:=300}"                           # seconds to wait for sshd

# ---------- helpers ----------
log()  { printf '%s\n' "$*" >&2; }
die()  { log "lam: $*"; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "missing dependency: $1"; }
need curl; need jq

API_STATUS=""; API_BODY=""
api() { # api METHOD PATH [JSON]
  [[ -n "${LAMBDA_API_KEY:-}" && "${LAMBDA_API_KEY}" != "secret_xxx" ]] || die "LAMBDA_API_KEY not set (env or $CONFIG_FILE). Create one at https://cloud.lambda.ai/api-keys"
  local method=$1 path=$2 body=${3:-} out
  local -a data=()
  [[ -n "$body" ]] && data=(--data-binary "$body")
  out=$(curl -sS --max-time 60 -X "$method" "$API$path" \
        -H "Authorization: Bearer $LAMBDA_API_KEY" -H 'Content-Type: application/json' \
        ${data[@]+"${data[@]}"} -w $'\n%{http_code}') || die "curl failed: $method $path"
  API_STATUS=${out##*$'\n'}
  API_BODY=${out%$'\n'*}
}
api_err() {
  jq -r '"lam: api error \(.error.code // "?"): \(.error.message // "")"
         + (if .error.suggestion then "\n  hint: \(.error.suggestion)" else "" end)' <<<"$API_BODY" 2>/dev/null \
    || log "lam: api error (HTTP $API_STATUS): $API_BODY"
}
api_ok() { api "$@"; if [[ "$API_STATUS" -ge 400 ]]; then api_err >&2; return 1; fi; printf '%s' "$API_BODY"; }
api_code() { jq -r '.error.code // ""' <<<"$API_BODY" 2>/dev/null || true; }

secs() { # 30m -> 1800
  local v=$1
  case "$v" in
    *h) echo $(( ${v%h} * 3600 ));; *m) echo $(( ${v%m} * 60 ));; *s) echo "${v%s}";; *) echo "$v";;
  esac
}
usd() { jq -r --argjson c "$1" -n '($c/100) | tostring | if test("\\.") then . else . + ".00" end' 2>/dev/null || echo "$1c"; }

SSH_OPTS=(-i "$LAM_SSH_PRIVATE_KEY" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=8 -o ServerAliveInterval=30)

# resolve ID-or-name-or-empty -> instance JSON
resolve_instance() {
  local q=${1:-} list
  list=$(api_ok GET /instances) || exit 1
  if [[ -z "$q" ]]; then
    local n; n=$(jq '[.data[] | select(.status != "terminated")] | length' <<<"$list")
    [[ "$n" -eq 0 ]] && die "no instances. Try: lam launch"
    [[ "$n" -gt 1 ]] && { log "lam: $n instances, pick one:"; jq -r '.data[] | "  \(.id)  \(.name // "-")  \(.status)  \(.ip // "-")"' <<<"$list" >&2; exit 1; }
    jq -c '[.data[] | select(.status != "terminated")][0]' <<<"$list"
  else
    local m; m=$(jq -c --arg q "$q" '[.data[] | select(.id == $q or .name == $q or (.id | startswith($q)))] | .[0] // empty' <<<"$list")
    [[ -n "$m" ]] || die "no instance matching '$q'"
    printf '%s' "$m"
  fi
}

save_state() { # id ip
  mkdir -p "$STATE_DIR"
  cat > "$STATE_DIR/last" <<EOT
export LAMBDA_ID=$1
export LAMBDA=$LAM_SSH_USER@$2
export LAMBDA_SSH_KEY=$LAM_SSH_PRIVATE_KEY
EOT
}

render_cloud_init() { # FILE-or-NAME -> path of rendered temp file (stdout)
  local src=$1
  [[ -f "$src" ]] || src="$LAM_HOME/cloud-init/$1.yaml"
  [[ -f "$src" ]] || die "cloud-init template not found: $1 (looked in $LAM_HOME/cloud-init/)"
  local tmp; tmp=$(mktemp)
  # substitute {{VAR}} from the environment; warn on unset
  local v
  for v in $(grep -oE '\{\{[A-Za-z_][A-Za-z0-9_]*\}\}' "$src" | tr -d '{}' | sort -u); do
    [[ -n "${!v:-}" ]] || log "lam: warning: template var {{$v}} is unset in your environment (rendered empty)"
  done
  perl -pe 's/\{\{([A-Za-z_][A-Za-z0-9_]*)\}\}/exists $ENV{$1} ? $ENV{$1} : ""/ge' "$src" > "$tmp"
  local bytes; bytes=$(wc -c < "$tmp")
  [[ "$bytes" -lt 1000000 ]] || die "user_data is $bytes bytes; Lambda caps it at 1MB"
  printf '%s' "$tmp"
}

# ---------- commands ----------
cmd_types() {
  local region="" avail=0
  while [[ $# -gt 0 ]]; do case "$1" in
    -r|--region) region=$2; shift 2;; -a|--available) avail=1; shift;; *) die "types: unknown arg $1";; esac; done
  local j; j=$(api_ok GET /instance-types) || exit 1
  {
    printf 'TYPE\tGPU\t$/HR\tVCPU\tRAM_GiB\tDISK_GiB\tREGIONS_WITH_CAPACITY\n'
    jq -r --arg region "$region" --argjson avail "$avail" '
      .data | to_entries[] | .value
      | select(($region == "") or (.regions_with_capacity_available | any(.name == $region)))
      | select(($avail == 0) or ((.regions_with_capacity_available | length) > 0))
      | [ .instance_type.name, .instance_type.gpu_description,
          (.instance_type.price_cents_per_hour/100),
          .instance_type.specs.vcpus, .instance_type.specs.memory_gib, .instance_type.specs.storage_gib,
          ((.regions_with_capacity_available | map(.name) | join(",")) | if . == "" then "-" else . end) ]
      | @tsv' <<<"$j" | sort
  } | column -t -s $'\t'
}

cmd_images() {
  local region=${1:-}
  local j; j=$(api_ok GET /images) || exit 1
  {
    printf 'FAMILY\tNAME\tVERSION\tARCH\tREGION\tID\n'
    jq -r --arg region "$region" '.data[] | select(($region == "") or (.region.name == $region))
      | [.family, .name, .version, .architecture, .region.name, .id] | @tsv' <<<"$j" | sort
  } | column -t -s $'\t'
}

cmd_keys() {
  local j; j=$(api_ok GET /ssh-keys) || exit 1
  { printf 'NAME\tID\tPUBLIC_KEY\n'; jq -r '.data[] | [.name, .id, (.public_key | .[0:40] + "…")] | @tsv' <<<"$j"; } | column -t -s $'\t'
}

cmd_ls() {
  local j; j=$(api_ok GET /instances) || exit 1
  {
    printf 'ID\tNAME\tTYPE\tREGION\tSTATUS\tIP\t$/HR\n'
    jq -r '.data[] | [.id, (.name // "-"), .instance_type.name, .region.name, .status, (.ip // "-"),
                      (.instance_type.price_cents_per_hour/100)] | @tsv' <<<"$j"
  } | column -t -s $'\t'
  jq -r '[.data[] | select(.status == "active" or .status == "booting") | .instance_type.price_cents_per_hour] | add // 0
         | "\nburn rate: $\(./100)/hr across running instances"' <<<"$j"
}

cmd_launch() {
  local type=$LAM_TYPE region=$LAM_REGION key=$LAM_SSH_KEY family=$LAM_IMAGE_FAMILY image_id="" name="" hostname=""
  local cloud_init=$LAM_CLOUD_INIT retry=0 wait=1 wait_init=1 dry=0
  while [[ $# -gt 0 ]]; do case "$1" in
    -t|--type) type=$2; shift 2;;
    -r|--region) region=$2; shift 2;;
    -k|--key) key=$2; shift 2;;
    -f|--image-family) family=$2; shift 2;;
    --image-id) image_id=$2; family=""; shift 2;;
    -n|--name) name=$2; shift 2;;
    --hostname) hostname=$2; shift 2;;
    -c|--cloud-init) cloud_init=$2; shift 2;;
    --no-cloud-init) cloud_init=""; shift;;
    --retry) retry=$(secs "$2"); shift 2;;
    --no-wait) wait=0; shift;;
    --no-wait-init) wait_init=0; shift;;
    --dry-run) dry=1; shift;;
    -h|--help) usage_launch; exit 0;;
    *) die "launch: unknown arg $1 (see: lam launch --help)";; esac; done

  # --- preflight: instance type + region + ssh key exist (cheap, saves a failed launch) ---
  local types; types=$(api_ok GET /instance-types) || exit 1
  local t; t=$(jq -c --arg t "$type" '.data[$t] // empty' <<<"$types")
  [[ -n "$t" ]] || die "unknown instance type '$type'. See: lam types"
  local price; price=$(jq -r '.instance_type.price_cents_per_hour' <<<"$t")
  local keys; keys=$(api_ok GET /ssh-keys) || exit 1
  jq -e --arg k "$key" '.data[] | select(.name == $k)' <<<"$keys" >/dev/null \
    || die "ssh key '$key' is not registered in Lambda. See: lam keys  (or add: lam keys add NAME ~/.ssh/x.pub)"
  [[ -f "$LAM_SSH_PRIVATE_KEY" ]] || log "lam: warning: LAM_SSH_PRIVATE_KEY=$LAM_SSH_PRIVATE_KEY not found; ssh/wait steps will fail"

  local udfile=/dev/null
  [[ -n "$cloud_init" ]] && udfile=$(render_cloud_init "$cloud_init")

  local body
  body=$(jq -n --arg region "$region" --arg type "$type" --arg key "$key" --arg family "$family" \
              --arg image_id "$image_id" --arg name "$name" --arg hostname "$hostname" --rawfile ud "$udfile" '
    {region_name:$region, instance_type_name:$type, ssh_key_names:[$key]}
    + (if $family   != "" then {image:{family:$family}} else {} end)
    + (if $image_id != "" then {image:{id:$image_id}} else {} end)
    + (if $name     != "" then {name:$name} else {} end)
    + (if $hostname != "" then {hostname:$hostname} else {} end)
    + (if $ud       != "" then {user_data:$ud} else {} end)')

  log "launch: $type in $region  image=${family:-$image_id}  key=$key  cloud-init=${cloud_init:-none}  \$$(usd "$price")/hr"
  if [[ "$dry" -eq 1 ]]; then jq '.user_data |= (if . then "<\(length) bytes>" else empty end)' <<<"$body"; exit 0; fi

  # --- launch, optionally retrying on insufficient capacity (rate limit: 1 launch call / 12s) ---
  local deadline=$(( $(date +%s) + retry )) id=""
  while :; do
    api POST /instance-operations/launch "$body"
    if [[ "$API_STATUS" -lt 400 ]]; then
      id=$(jq -r '.data.instance_ids[0]' <<<"$API_BODY"); break
    fi
    local code; code=$(api_code)
    if [[ "$code" == "instance-operations/launch/insufficient-capacity" && $(date +%s) -lt $deadline ]]; then
      log "  no $type capacity in $region ($(date +%H:%M:%S)); retrying in 15s (until $(date -r "$deadline" +%H:%M:%S 2>/dev/null || echo deadline))"
      sleep 15; continue
    fi
    api_err >&2
    [[ "$code" == "instance-operations/launch/insufficient-capacity" ]] && log "  tip: lam types -a   (see where capacity is)   or   lam launch --retry 30m"
    exit 1
  done
  log "launched instance $id"
  [[ "$wait" -eq 1 ]] || { echo "$id"; exit 0; }

  # --- wait for active + IP ---
  local start=$(date +%s) inst status ip
  while :; do
    inst=$(api_ok GET "/instances/$id") || exit 1
    status=$(jq -r '.data.status' <<<"$inst"); ip=$(jq -r '.data.ip // ""' <<<"$inst")
    [[ "$status" == "active" && -n "$ip" ]] && break
    [[ "$status" == "terminated" || "$status" == "unhealthy" ]] && die "instance went $status"
    [[ $(( $(date +%s) - start )) -lt "$LAM_ACTIVE_TIMEOUT" ]] || die "timed out waiting for active (status=$status). lam ls"
    log "  status=$status ip=${ip:--} ($(( $(date +%s) - start ))s)"; sleep 10
  done
  save_state "$id" "$ip"
  log "active: $LAM_SSH_USER@$ip  (booted in $(( $(date +%s) - start ))s)"

  # --- wait for sshd ---
  local sstart=$(date +%s)
  until ssh "${SSH_OPTS[@]}" -o BatchMode=yes "$LAM_SSH_USER@$ip" true 2>/dev/null; do
    [[ $(( $(date +%s) - sstart )) -lt "$LAM_SSH_TIMEOUT" ]] || die "ssh not reachable after ${LAM_SSH_TIMEOUT}s: ssh -i $LAM_SSH_PRIVATE_KEY $LAM_SSH_USER@$ip"
    sleep 5
  done
  log "ssh ready ($(( $(date +%s) - sstart ))s)"

  # --- wait for cloud-init (the bootstrap) ---
  if [[ -n "$cloud_init" && "$wait_init" -eq 1 ]]; then
    log "waiting for cloud-init ($cloud_init) … tail with: lam logs $id"
    if ssh "${SSH_OPTS[@]}" "$LAM_SSH_USER@$ip" 'sudo cloud-init status --wait >/dev/null 2>&1; rc=$?; cloud-init status; exit $rc'; then
      log "cloud-init done"
    else
      log "cloud-init reported errors; last lines of /var/log/cloud-init-output.log:"
      ssh "${SSH_OPTS[@]}" "$LAM_SSH_USER@$ip" 'sudo tail -n 30 /var/log/cloud-init-output.log' >&2 || true
    fi
  fi

  log ""
  log "ready in $(( $(date +%s) - start ))s.  ssh:   lam ssh        env:   eval \"\$(lam env)\"    kill:   lam rm $id"
  echo "ssh -i $LAM_SSH_PRIVATE_KEY $LAM_SSH_USER@$ip"
}

cmd_wait() { # re-enter the wait phase for an existing instance
  local inst; inst=$(resolve_instance "${1:-}")
  local id ip; id=$(jq -r .id <<<"$inst"); ip=$(jq -r '.ip // ""' <<<"$inst")
  [[ -n "$ip" ]] || die "instance $id has no IP yet (status=$(jq -r .status <<<"$inst"))"
  until ssh "${SSH_OPTS[@]}" -o BatchMode=yes "$LAM_SSH_USER@$ip" true 2>/dev/null; do log "  waiting for ssh…"; sleep 5; done
  ssh "${SSH_OPTS[@]}" "$LAM_SSH_USER@$ip" 'sudo cloud-init status --wait >/dev/null 2>&1; cloud-init status'
  save_state "$id" "$ip"
}

cmd_ssh() {
  local q=""; [[ $# -gt 0 && "$1" != "--" ]] && { q=$1; shift; }
  [[ "${1:-}" == "--" ]] && shift
  local inst; inst=$(resolve_instance "$q")
  local ip; ip=$(jq -r '.ip // ""' <<<"$inst"); [[ -n "$ip" ]] || die "no IP yet (status=$(jq -r .status <<<"$inst"))"
  exec ssh "${SSH_OPTS[@]}" -t "$LAM_SSH_USER@$ip" "$@"
}

cmd_env() {
  local inst; inst=$(resolve_instance "${1:-}")
  local id ip; id=$(jq -r .id <<<"$inst"); ip=$(jq -r '.ip // ""' <<<"$inst")
  [[ -n "$ip" ]] || die "instance $id has no IP yet (status=$(jq -r .status <<<"$inst")); try: lam wait"
  save_state "$id" "$ip"; cat "$STATE_DIR/last"
}

cmd_logs() {
  local inst; inst=$(resolve_instance "${1:-}")
  local ip; ip=$(jq -r '.ip // ""' <<<"$inst")
  exec ssh "${SSH_OPTS[@]}" -t "$LAM_SSH_USER@$ip" 'sudo tail -n 50 -f /var/log/cloud-init-output.log'
}

cmd_rm() {
  local yes=0 all=0; local -a ids=()
  while [[ $# -gt 0 ]]; do case "$1" in
    -y|--yes) yes=1; shift;; -a|--all) all=1; shift;; *) ids+=("$1"); shift;; esac; done
  local list; list=$(api_ok GET /instances) || exit 1
  local -a targets=()
  if [[ "$all" -eq 1 ]]; then
    local i; for i in $(jq -r '.data[] | select(.status != "terminated" and .status != "terminating") | .id' <<<"$list"); do targets+=("$i"); done
  elif [[ ${#ids[@]} -eq 0 ]]; then  # bash 3.2: empty arrays need the ${a[@]+...} guard under set -u
    targets=("$(jq -r .id <<<"$(resolve_instance "")")")
  else
    local q; for q in ${ids[@]+"${ids[@]}"}; do targets+=("$(jq -r .id <<<"$(resolve_instance "$q")")"); done
  fi
  [[ ${#targets[@]} -gt 0 ]] || { log "nothing to terminate"; exit 0; }  # from here targets is non-empty
  log "terminate:"; jq -r --argjson t "$(printf '%s\n' "${targets[@]}" | jq -R . | jq -s .)" \
    '.data[] | select(.id as $i | $t | index($i)) | "  \(.id)  \(.name // "-")  \(.instance_type.name)  \(.ip // "-")"' <<<"$list" >&2
  if [[ "$yes" -ne 1 ]]; then read -r -p "confirm [y/N] " a; [[ "$a" == y || "$a" == Y ]] || exit 1; fi
  local body; body=$(printf '%s\n' "${targets[@]}" | jq -R . | jq -s '{instance_ids: .}')
  api_ok POST /instance-operations/terminate "$body" | jq -r '.data.terminated_instances[] | "terminating \(.id) \(.name // "")"'
  rm -f "$STATE_DIR/last"
}

cmd_keys_add() { # NAME PUBKEY_FILE
  local name=${1:-} file=${2:-}
  [[ -n "$name" && -f "$file" ]] || die "usage: lam keys add NAME ~/.ssh/key.pub"
  api_ok POST /ssh-keys "$(jq -n --arg n "$name" --rawfile k "$file" '{name:$n, public_key:($k|rtrimstr("\n"))}')" | jq -r '"added key \(.data.name) (\(.data.id))"'
}

cmd_render() { # print a cloud-init template with {{VARS}} filled from the environment
  [[ -n "${1:-}" ]] || die "usage: lam render NAME|FILE"
  local f; f=$(render_cloud_init "$1"); cat "$f"; rm -f "$f"
}

cmd_config() {
  case "${1:-}" in
    init)
      mkdir -p "$(dirname "$CONFIG_FILE")"
      [[ -f "$CONFIG_FILE" ]] && die "$CONFIG_FILE already exists"
      cp "$LAM_HOME/config.example" "$CONFIG_FILE"; chmod 600 "$CONFIG_FILE"
      log "wrote $CONFIG_FILE — edit it and set LAMBDA_API_KEY";;
    *)
      cat <<EOT
config file:          $CONFIG_FILE $([[ -f "$CONFIG_FILE" ]] && echo "(present)" || echo "(absent; run: lam config init)")
LAMBDA_API_KEY:       $([[ -n "${LAMBDA_API_KEY:-}" && "${LAMBDA_API_KEY}" != "secret_xxx" ]] && echo "set (${LAMBDA_API_KEY:0:6}…)" || echo "NOT SET — edit $CONFIG_FILE")
LAM_REGION:           $LAM_REGION
LAM_TYPE:             $LAM_TYPE
LAM_SSH_KEY:          $LAM_SSH_KEY
LAM_SSH_PRIVATE_KEY:  $LAM_SSH_PRIVATE_KEY $([[ -f "$LAM_SSH_PRIVATE_KEY" ]] || echo "(MISSING)")
LAM_IMAGE_FAMILY:     $LAM_IMAGE_FAMILY
LAM_CLOUD_INIT:       ${LAM_CLOUD_INIT:-(none)}
templates:            $(ls "$LAM_HOME/cloud-init" 2>/dev/null | sed 's/\.yaml$//' | tr '\n' ' ')
EOT
      ;;
  esac
}

usage_launch() {
  cat <<EOT
usage: lam launch [options]
  -t, --type TYPE            instance type            (default: $LAM_TYPE)
  -r, --region REGION        region                   (default: $LAM_REGION)
  -k, --key NAME             Lambda ssh key name      (default: $LAM_SSH_KEY)
  -f, --image-family FAMILY  image family             (default: $LAM_IMAGE_FAMILY)
      --image-id ID          specific image id (overrides family)
  -n, --name NAME            instance name (shown in console / lam ls)
      --hostname HOST        instance hostname
  -c, --cloud-init NAME|FILE cloud-init template: name from $LAM_HOME/cloud-init/ or a path
      --no-cloud-init        ignore LAM_CLOUD_INIT default
      --retry DURATION       keep retrying on insufficient capacity, e.g. 30m, 2h
      --no-wait              return the instance id immediately, don't wait for boot/ssh
      --no-wait-init         wait for ssh but not for cloud-init to finish
      --dry-run              print the launch request body and exit
Template vars {{VAR}} are filled from your environment (e.g. HF_TOKEN, MODEL).
EOT
}

usage() {
  cat <<EOT
lam $VERSION — Lambda Cloud from the terminal

  lam launch [opts]        launch, wait for ssh + cloud-init, print the ssh command   (lam launch --help)
  lam ls                   running instances + burn rate
  lam ssh [ID|NAME] [-- CMD]  ssh in (no arg = the only running instance)
  lam env [ID]             print 'export LAMBDA=…' lines (compatible with class .env files)
  lam wait [ID]            block until ssh + cloud-init are done
  lam logs [ID]            tail cloud-init output on the instance
  lam rm [ID…|--all] [-y]  terminate
  lam types [-r REGION] [-a]   instance types, prices, where capacity exists
  lam images [REGION]      image families / ids
  lam keys | keys add NAME FILE.pub
  lam render NAME|FILE     print a cloud-init template with {{VARS}} filled in
  lam config [init]        show effective config / create $CONFIG_FILE

Defaults come from env or $CONFIG_FILE. LAMBDA_API_KEY is required.
EOT
}

main() {
  local cmd=${1:-help}; shift || true
  case "$cmd" in
    launch|up)   cmd_launch "$@";;
    ls|list|ps)  cmd_ls "$@";;
    ssh)         cmd_ssh "$@";;
    env)         cmd_env "$@";;
    wait)        cmd_wait "$@";;
    logs|log)    cmd_logs "$@";;
    rm|terminate|down|kill) cmd_rm "$@";;
    types)       cmd_types "$@";;
    images)      cmd_images "$@";;
    keys)        if [[ "${1:-}" == "add" ]]; then shift; cmd_keys_add "$@"; else cmd_keys; fi;;
    config)      cmd_config "$@";;
    render)      cmd_render "$@";;
    version)     echo "lam $VERSION";;
    help|-h|--help) usage;;
    *) die "unknown command '$cmd' (lam help)";;
  esac
}
main "$@"
