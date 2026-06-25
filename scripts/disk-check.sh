#!/bin/bash
# ============================================================
# bitmagnet — Disk Usage Guardian (M0.6)
# ============================================================
# Run this as a cron job on your Docker host to monitor the
# Postgres data volume and stop the DHT crawler before disk
# exhaustion kills Postgres non-gracefully.
#
# Cron example (check every 15 minutes):
#   */15 * * * * /home/o51r15/docker/bitmagnet/scripts/disk-check.sh >> /var/log/bitmagnet-disk.log 2>&1
#
# Usage:
#   ./disk-check.sh [threshold_percent]   default threshold: 85
# ============================================================

THRESHOLD=${1:-85}
DATA_DIR="/home/o51r15/docker/bitmagnet/data/postgres"
BITMAGNET_CONTAINER="bitmagnet"
LOG_PREFIX="[bitmagnet disk-check]"

# Get current disk usage percentage for the data directory
DISK_USE=$(df "$DATA_DIR" 2>/dev/null | awk 'NR==2 {print $5}' | tr -d '%')

if [ -z "$DISK_USE" ]; then
    echo "$LOG_PREFIX ERROR: could not read disk usage for $DATA_DIR"
    exit 1
fi

echo "$LOG_PREFIX Disk usage: ${DISK_USE}% (threshold: ${THRESHOLD}%)"

if [ "$DISK_USE" -ge "$THRESHOLD" ]; then
    echo "$LOG_PREFIX WARNING: disk usage ${DISK_USE}% >= threshold ${THRESHOLD}%"
    echo "$LOG_PREFIX Stopping DHT crawler to prevent disk exhaustion..."

    # Stop just the bitmagnet container — Postgres keeps running so data
    # is not corrupted. The crawler can be restarted manually after cleanup.
    if docker stop "$BITMAGNET_CONTAINER" 2>/dev/null; then
        echo "$LOG_PREFIX $BITMAGNET_CONTAINER stopped successfully."
        echo "$LOG_PREFIX Free up space, then restart with: docker start $BITMAGNET_CONTAINER"
    else
        echo "$LOG_PREFIX ERROR: failed to stop $BITMAGNET_CONTAINER (not running?)"
    fi
    exit 2
fi

exit 0
