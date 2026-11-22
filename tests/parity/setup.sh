#!/usr/bin/env bash
# Start (or restart) the two local containers the parity harness needs.
# Bound to 127.0.0.1 only. Remove with: docker rm -f lm-parity-pg lm-parity-mail
set -euo pipefail
docker rm -f lm-parity-pg lm-parity-mail >/dev/null 2>&1 || true
docker run -d --name lm-parity-pg -p 127.0.0.1:55432:5432 \
	-e POSTGRES_USER=listmonk -e POSTGRES_PASSWORD=listmonk -e POSTGRES_DB=listmonk postgres:18-alpine >/dev/null
docker run -d --name lm-parity-mail -p 127.0.0.1:51025:1025 -p 127.0.0.1:58025:8025 axllent/mailpit >/dev/null
for _ in $(seq 50); do
	docker exec lm-parity-pg pg_isready -U listmonk >/dev/null 2>&1 && break
	sleep 0.2
done
echo "parity containers up"
