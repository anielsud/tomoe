#!/usr/bin/env bash
#
# tomoe-backup.sh — periodic backup of Tomoe meeting sessions to a remote host.
#
# What it does
#   1. rsync the entire sessions directory (transcripts + audio) to a remote path
#      over SSH. Incremental — only new/changed files are transferred.
#   2. After a successful sync, prune local audio files (audio.*) older than
#      $AUDIO_RETENTION_DAYS days, but only when the remote copy exists AND its
#      byte size matches the local file. session.json is never deleted.
#
# Fails safe
#   - Any non-zero rsync exit skips the prune phase entirely.
#   - Prune requires a sibling session.json to exist; orphan audio is kept.
#   - Remote size mismatch or missing remote file → local audio is kept.
#
# Configuration
#   Resolved in this order (first hit wins):
#     1. Path in $TOMOE_BACKUP_CONFIG (if set)
#     2. ${XDG_CONFIG_HOME:-$HOME/.config}/tomoe/backup.env
#     3. Environment variables already exported
#
# Required
#   REMOTE_USER   SSH user on the backup host
#   REMOTE_HOST   backup hostname
#   REMOTE_DIR    absolute destination path on the backup host
#
# Optional (with defaults)
#   SESSIONS_DIR             $HOME/.local/share/tomoe/sessions
#   AUDIO_RETENTION_DAYS     7
#   SSH_KEY                  (unset → ssh picks default key)
#   LOG_DIR                  ${XDG_STATE_HOME:-$HOME/.local/state}/tomoe-backup
#   LOCK_FILE                /tmp/tomoe-backup.lock
#   LOG_MAX_BYTES            5242880   (5 MiB — rotate above this)
#   LOG_ROTATE_KEEP          5         (rotated copies retained)
#
# Requirements
#   - rsync locally and on the remote host
#   - SSH key auth to REMOTE_USER@REMOTE_HOST (cron cannot type a password)
#   - flock (util-linux) — used for single-instance locking

set -euo pipefail

# ---- Config resolution -------------------------------------------------------
CONFIG_DEFAULT="${XDG_CONFIG_HOME:-$HOME/.config}/tomoe/backup.env"
CONFIG_FILE="${TOMOE_BACKUP_CONFIG:-$CONFIG_DEFAULT}"
if [[ -f "$CONFIG_FILE" ]]; then
  # shellcheck disable=SC1090
  source "$CONFIG_FILE"
fi

: "${SESSIONS_DIR:=$HOME/.local/share/tomoe/sessions}"
: "${AUDIO_RETENTION_DAYS:=7}"
: "${LOG_DIR:=${XDG_STATE_HOME:-$HOME/.local/state}/tomoe-backup}"
: "${LOCK_FILE:=/tmp/tomoe-backup.lock}"
: "${LOG_MAX_BYTES:=5242880}"
: "${LOG_ROTATE_KEEP:=5}"

missing=()
for v in REMOTE_USER REMOTE_HOST REMOTE_DIR; do
  [[ -n "${!v:-}" ]] || missing+=("$v")
done
if (( ${#missing[@]} > 0 )); then
  echo "tomoe-backup: missing required config: ${missing[*]}" >&2
  echo "Set them in $CONFIG_FILE or export them before running." >&2
  echo "See scripts/backup/tomoe-backup.env.example for a template." >&2
  exit 2
fi

LOG_FILE="$LOG_DIR/backup.log"
mkdir -p "$LOG_DIR"

# ---- Log rotation ------------------------------------------------------------
if [[ -f "$LOG_FILE" ]]; then
  size=$(stat -c %s "$LOG_FILE" 2>/dev/null || echo 0)
  if (( size > LOG_MAX_BYTES )); then
    mv "$LOG_FILE" "$LOG_FILE.$(date +%Y%m%d-%H%M%S)"
    # shellcheck disable=SC2012
    ls -1t "$LOG_FILE".* 2>/dev/null | tail -n +$((LOG_ROTATE_KEEP + 1)) | xargs -r rm --
  fi
fi

exec >>"$LOG_FILE" 2>&1
log() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*"; }

# ---- Lock --------------------------------------------------------------------
exec 9>"$LOCK_FILE"
if ! flock -n 9; then
  log "another instance is running; exiting"
  exit 0
fi

log "=== tomoe-backup start ==="
log "sessions dir : $SESSIONS_DIR"
log "destination  : $REMOTE_USER@$REMOTE_HOST:$REMOTE_DIR"
log "retention    : $AUDIO_RETENTION_DAYS days"

if [[ ! -d "$SESSIONS_DIR" ]]; then
  log "ERROR: sessions dir not found — aborting"
  exit 1
fi

SSH_OPTS=(-o BatchMode=yes -o ServerAliveInterval=60 -o ConnectTimeout=30)
[[ -n "${SSH_KEY:-}" ]] && SSH_OPTS+=(-i "$SSH_KEY")

# ---- Step 1: rsync -----------------------------------------------------------
log "rsync starting..."
rsync_rc=0
rsync -az --update --partial --stats \
  -e "ssh ${SSH_OPTS[*]}" \
  "$SESSIONS_DIR/" \
  "$REMOTE_USER@$REMOTE_HOST:$REMOTE_DIR/" || rsync_rc=$?

if (( rsync_rc != 0 )); then
  log "ERROR: rsync exited $rsync_rc — skipping prune phase"
  exit "$rsync_rc"
fi
log "rsync completed OK"

# ---- Step 2: prune old local audio, verify each against the remote -----------
log "prune phase: scanning audio files older than $AUDIO_RETENTION_DAYS days..."

mapfile -d '' candidates < <(
  find "$SESSIONS_DIR" -mindepth 2 -maxdepth 2 -type f \
       -name 'audio.*' -mtime "+${AUDIO_RETENTION_DAYS}" -print0
)

pruned=0
kept_no_session=0
kept_missing=0
kept_size_mismatch=0

if (( ${#candidates[@]} == 0 )); then
  log "no audio files eligible for prune"
else
  log "eligible candidates: ${#candidates[@]}"

  # One SSH round-trip: remote reports size (or MISSING) for every candidate.
  remote_script=""
  for local_path in "${candidates[@]}"; do
    session_id=$(basename "$(dirname "$local_path")")
    filename=$(basename "$local_path")
    key="$session_id/$filename"
    remote_script+=$'printf "%s " "'"$key"'"; stat -c %s "'"$REMOTE_DIR/$key"'" 2>/dev/null || echo MISSING'$'\n'
  done

  declare -A remote_size
  while IFS=' ' read -r k v; do
    remote_size["$k"]="$v"
  done < <(ssh "${SSH_OPTS[@]}" "$REMOTE_USER@$REMOTE_HOST" "$remote_script")

  for local_path in "${candidates[@]}"; do
    session_dir=$(dirname "$local_path")
    session_id=$(basename "$session_dir")
    filename=$(basename "$local_path")
    key="$session_id/$filename"

    if [[ ! -f "$session_dir/session.json" ]]; then
      log "KEEP (no session.json): $key"
      kept_no_session=$((kept_no_session + 1))
      continue
    fi

    rsize="${remote_size[$key]:-MISSING}"
    lsize=$(stat -c %s "$local_path")

    if [[ "$rsize" == "MISSING" ]] || [[ -z "$rsize" ]]; then
      log "KEEP (not on remote): $key"
      kept_missing=$((kept_missing + 1))
      continue
    fi

    if [[ "$rsize" != "$lsize" ]]; then
      log "KEEP (size mismatch: remote=$rsize local=$lsize): $key"
      kept_size_mismatch=$((kept_size_mismatch + 1))
      continue
    fi

    rm -- "$local_path"
    log "PRUNED: $key (${lsize} bytes)"
    pruned=$((pruned + 1))
  done
fi

log "prune summary: pruned=$pruned kept_missing=$kept_missing kept_size_mismatch=$kept_size_mismatch kept_no_session=$kept_no_session"
log "=== tomoe-backup done ==="
