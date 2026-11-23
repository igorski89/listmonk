#!/usr/bin/env bash
# Start (or restart) the two local containers the parity harness needs.
# OIDC=1 also starts Keycloak with the realm in oidc/realm.json.
# Bound to 127.0.0.1 only. Remove with: docker rm -f lm-parity-pg lm-parity-mail lm-parity-kc
set -euo pipefail
docker rm -f lm-parity-pg lm-parity-mail >/dev/null 2>&1 || true
docker run -d --name lm-parity-pg -p 127.0.0.1:55432:5432 \
	-e POSTGRES_USER=listmonk -e POSTGRES_PASSWORD=listmonk -e POSTGRES_DB=listmonk postgres:18-alpine >/dev/null
docker run -d --name lm-parity-mail -p 127.0.0.1:51025:1025 -p 127.0.0.1:58025:8025 axllent/mailpit >/dev/null
for _ in $(seq 50); do
	docker exec lm-parity-pg pg_isready -U listmonk >/dev/null 2>&1 && break
	sleep 0.2
done
if [ "${OIDC:-}" = 1 ]; then
	docker rm -f lm-parity-kc >/dev/null 2>&1 || true
	docker run -d --name lm-parity-kc -p 127.0.0.1:58080:8080 \
		-e KC_BOOTSTRAP_ADMIN_USERNAME=admin -e KC_BOOTSTRAP_ADMIN_PASSWORD=admin \
		-e KC_HOSTNAME=http://127.0.0.1:58080 \
		-v "$(cd "$(dirname "$0")" && pwd)/oidc/realm.json:/opt/keycloak/data/import/realm.json:ro" \
		quay.io/keycloak/keycloak:26.6.2 start-dev --import-realm >/dev/null
	for _ in $(seq 180); do
		curl -sf http://127.0.0.1:58080/realms/parity/.well-known/openid-configuration >/dev/null && break
		sleep 1
	done
fi
echo "parity containers up"
