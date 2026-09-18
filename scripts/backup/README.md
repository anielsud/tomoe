# Session backup

Periodic rsync-based backup of Tomoe meeting sessions (transcripts + audio) to
a remote host over SSH, with **verified** local audio pruning after a
configurable retention window. Transcript `session.json` files are always kept
locally.

## What it does

Each run:

1. **rsync** the entire sessions directory to the configured remote path.
   Incremental — only new or changed files cross the wire.
2. If (and only if) rsync exited zero, **prune** local `audio.*` files older
   than `AUDIO_RETENTION_DAYS`. For every candidate, one batched SSH call
   verifies the remote copy exists **and** its byte size matches. If either
   check fails, the local file stays and the run logs `KEEP (reason)`.

Fails safe: rsync errors skip the prune phase entirely. Orphan audio files
(no sibling `session.json`) are always kept.

## Requirements

- **Local**: `rsync`, `ssh`, `flock` (util-linux).
- **Remote**: `rsync` and standard `stat`.
- **SSH key auth** from wherever cron/systemd runs, to the remote user on the
  backup host. Cron cannot type an interactive password.

## Install

```sh
# 1. Put the script somewhere on your PATH.
install -m 0755 scripts/backup/tomoe-backup.sh ~/.local/bin/tomoe-backup.sh

# 2. Create your config from the example.
mkdir -p ~/.config/tomoe
cp scripts/backup/tomoe-backup.env.example ~/.config/tomoe/backup.env
$EDITOR ~/.config/tomoe/backup.env    # fill in REMOTE_USER, REMOTE_HOST, REMOTE_DIR

# 3. Verify the SSH path works non-interactively.
ssh -o BatchMode=yes "$REMOTE_USER@$REMOTE_HOST" 'echo ok'

# 4. Do the first run manually. On a first install this uploads everything;
#    later runs push only the delta.
~/.local/bin/tomoe-backup.sh
tail -f ~/.local/state/tomoe-backup/backup.log
```

## Schedule it

### cron

Add via `crontab -e`:

```cron
# Weekly, Monday 09:00 local time
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
0 9 * * 1 /home/YOU/.local/bin/tomoe-backup.sh
```

The explicit `PATH=` line matters — cron's default is minimal and can miss
`rsync` or `flock`.

### systemd (user timer)

`~/.config/systemd/user/tomoe-backup.service`:

```ini
[Unit]
Description=Tomoe sessions backup

[Service]
Type=oneshot
ExecStart=%h/.local/bin/tomoe-backup.sh
```

`~/.config/systemd/user/tomoe-backup.timer`:

```ini
[Unit]
Description=Weekly Tomoe backup

[Timer]
OnCalendar=Mon 09:00
Persistent=true

[Install]
WantedBy=timers.target
```

Enable:

```sh
systemctl --user daemon-reload
systemctl --user enable --now tomoe-backup.timer
```

## Configuration

Config resolution, first hit wins:

1. Path in `$TOMOE_BACKUP_CONFIG`
2. `${XDG_CONFIG_HOME:-$HOME/.config}/tomoe/backup.env`
3. Environment variables already exported

See [`tomoe-backup.env.example`](./tomoe-backup.env.example) for the full list.

### Required

| Variable | Meaning |
|---|---|
| `REMOTE_USER` | SSH user on the backup host |
| `REMOTE_HOST` | Backup hostname |
| `REMOTE_DIR` | Absolute destination path on the backup host |

### Optional (with defaults)

| Variable | Default | Meaning |
|---|---|---|
| `SESSIONS_DIR` | `$HOME/.local/share/tomoe/sessions` | Where Tomoe stores sessions |
| `AUDIO_RETENTION_DAYS` | `7` | Prune local audio older than this |
| `SSH_KEY` | *(unset)* | Explicit `-i` key path, if needed |
| `LOG_DIR` | `${XDG_STATE_HOME:-$HOME/.local/state}/tomoe-backup` | Log location |
| `LOCK_FILE` | `/tmp/tomoe-backup.lock` | Single-instance lock |
| `LOG_MAX_BYTES` | `5242880` (5 MiB) | Rotate log above this |
| `LOG_ROTATE_KEEP` | `5` | Rotated copies retained |

## Log

Each run appends to `LOG_DIR/backup.log`:

```
[2026-09-18 19:28:01] === tomoe-backup start ===
[2026-09-18 19:28:01] sessions dir : /home/…/tomoe/sessions
[2026-09-18 19:28:01] destination  : you@host:/srv/backups/tomoe-sessions
[2026-09-18 19:28:01] retention    : 7 days
[2026-09-18 19:28:01] rsync starting...
[2026-09-18 19:28:53] rsync completed OK
[2026-09-18 19:28:53] eligible candidates: 620
[2026-09-18 19:28:53] PRUNED: <uuid>/audio.m4a (…)
…
[2026-09-18 19:28:53] prune summary: pruned=620 kept_missing=0 kept_size_mismatch=0 kept_no_session=1
[2026-09-18 19:28:53] === tomoe-backup done ===
```

Rotation is automatic once the file crosses `LOG_MAX_BYTES`; the newest
`LOG_ROTATE_KEEP` rotated copies are kept.

## Recovery

Restoring is a plain rsync in the other direction:

```sh
rsync -avz "$REMOTE_USER@$REMOTE_HOST:$REMOTE_DIR/" \
      "$HOME/.local/share/tomoe/sessions/"
```

Since transcripts are never deleted locally, only audio needs restoration in
practice.
