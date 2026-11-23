#!/bin/sh
# Wait until a URL responds, for up to 60 seconds.
set -eu
url="$1"
for _ in $(seq 1 60); do
  if curl -fsS -o /dev/null "$url"; then exit 0; fi
  sleep 1
done
echo "timed out waiting for $url" >&2
docker logs "$(docker ps -lq)" >&2 || true
exit 1
