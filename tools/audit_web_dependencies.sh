#!/bin/sh
set -eu

# Scan the exact Bun lockfile with the pinned OSV-Scanner installed by CI.
# Permit one fresh bounded request after a transport failure, but never convert
# a vulnerability result or repeated transport failure into success.
attempt=1
while [ "$attempt" -le 2 ]; do
	if timeout 300s osv-scanner scan source --lockfile=bun.lock; then
		exit 0
	else
		status=$?
	fi
	if [ "$attempt" -eq 2 ]; then
		echo "web dependency audit failed after two bounded attempts" >&2
		exit "$status"
	fi
	echo "web dependency audit attempt failed; retrying once" >&2
	sleep 5
	attempt=$((attempt + 1))
done
