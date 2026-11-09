#!/bin/bash
# Reopens Tomoe when it died instead of quitting, so a crash doesn't leave
# the following days' meetings unrecorded. Run every minute by the launch
# agent `make install-relaunch-mac` installs.
#
# A clean quit ends with a "Tomoe stopped" banner in the log; a start
# banner with nothing after it and no tomoe-gui process means the app died.
# At most three relaunches an hour, so a build that crashes at start
# doesn't loop.
#
# Overridable for testing: TOMOE_LOG, TOMOE_RELAUNCH_STATE,
# TOMOE_RUNNING_CMD, TOMOE_OPEN_CMD.

LOG="${TOMOE_LOG:-$HOME/Library/Logs/Tomoe/tomoe-gui.log}"
STATE="${TOMOE_RELAUNCH_STATE:-$HOME/Library/Logs/Tomoe/relaunch.times}"
RUNNING_CMD="${TOMOE_RUNNING_CMD:-pgrep -xq tomoe-gui}"
OPEN_CMD="${TOMOE_OPEN_CMD:-open -a Tomoe}"
MAX_PER_HOUR=3

$RUNNING_CMD && exit 0
[ -f "$LOG" ] || exit 0

last=$(grep -E '===== Tomoe (started|stopped) ' "$LOG" | tail -n 1)
case "$last" in
*"Tomoe started"*) ;;
*) exit 0 ;; # quit cleanly, or never started
esac

now=$(date +%s)
recent=0
if [ -f "$STATE" ]; then
	recent=$(awk -v cutoff=$((now - 3600)) '$1 > cutoff' "$STATE" | wc -l | tr -d ' ')
fi
if [ "$recent" -ge "$MAX_PER_HOUR" ]; then
	exit 0
fi
echo "$now" >>"$STATE"
echo "$(date '+%Y-%m-%d %H:%M:%S.000') ===== relaunching Tomoe: it stopped without quitting =====" >>"$LOG"
$OPEN_CMD
