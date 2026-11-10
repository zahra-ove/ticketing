#!/usr/bin/env bash
# Guided tour of the platform through the gateway. Needs `make services-up`.
# Uses only curl and sed, so it runs in Git Bash, macOS and Linux alike.
set -euo pipefail

GW=${GATEWAY:-http://localhost:8000}
ADMIN_PASSWORD=${ADMIN_PASSWORD:-admin-secret}
DEMO_PASSWORD=${DEMO_PASSWORD:-demo-secret}

step() { printf '\n\033[1;36m== %s\033[0m\n' "$*"; }
say() { printf '   %s\n' "$*"; }
# First occurrence of a top-level-looking field; good enough for these responses.
json_field() { grep -oE "\"$1\":\"?[^\",}]*" | head -1 | sed -E "s/\"$1\":\"?//"; }
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

login() {
	curl -sf "$GW/v1/token" -d "{\"username\":\"$1\",\"password\":\"$2\"}" | json_field access_token
}

wait_status() { # booking token wanted
	local st=""
	for _ in $(seq 1 40); do
		st=$(curl -s "$GW/v1/bookings/$1" -H "Authorization: Bearer $2" | json_field status)
		[ "$st" = "$3" ] && break
		sleep 0.5
	done
	echo "$st"
}

new_event() { # admin-token price-in-cents
	local ev
	ev=$(curl -sf "$GW/v1/events" -H "Authorization: Bearer $1" -d "{
	  \"title\": \"Demo concert\", \"venue\": \"Main hall\",
	  \"starts_at\": \"$(date -u -d '+30 days' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v+30d +%Y-%m-%dT%H:%M:%SZ)\",
	  \"sections\": [{\"name\": \"A\", \"rows\": 1, \"seats_per_row\": 4, \"price_cents\": $2}]
	}" | json_field id)
	curl -sf -o /dev/null -X POST "$GW/v1/events/$ev/publish" -H "Authorization: Bearer $1"
	echo "$ev"
}

step "1. Log in (the gateway is the only entry point)"
ADMIN=$(login admin "$ADMIN_PASSWORD")
ALICE=$(login alice "$DEMO_PASSWORD")
BOB=$(login bob "$DEMO_PASSWORD")
say "admin, alice and bob have Ed25519-signed JWTs"
say "wrong password -> $(code "$GW/v1/token" -d '{"username":"alice","password":"nope"}')"

step "2. Only admins can change the catalog"
say "alice creates an event -> $(code "$GW/v1/events" -H "Authorization: Bearer $ALICE" -d '{}')"
say "forged identity headers, no token -> $(code "$GW/v1/events" -H 'X-User-Role: admin' -H 'X-Customer-ID: 00000000-0000-4000-8000-000000000000' -d '{}')"
EVENT=$(new_event "$ADMIN" 4900)
say "admin created and published event $EVENT"
SEATS=($(curl -s "$GW/v1/events/$EVENT/seats" | grep -oE '"id":"[^"]+"' | cut -d'"' -f4))
say "the event has ${#SEATS[@]} seats; reading the catalog needs no token"

step "3. Alice holds two seats; Bob wants one of them"
HOLD=$(curl -s "$GW/v1/holds" -H "Authorization: Bearer $ALICE" \
	-d "{\"event_id\":\"$EVENT\",\"seat_ids\":[\"${SEATS[0]}\",\"${SEATS[1]}\"]}" | json_field id)
say "alice holds seats 1+2 (hold $HOLD)"
say "bob tries seats 2+3 -> $(code "$GW/v1/holds" -H "Authorization: Bearer $BOB" -d "{\"event_id\":\"$EVENT\",\"seat_ids\":[\"${SEATS[1]}\",\"${SEATS[2]}\"]}") (all-or-nothing: seat 3 stays free)"

step "4. Alice books; the payment saga runs over Kafka"
BOOKING=$(curl -s "$GW/v1/bookings" -H "Authorization: Bearer $ALICE" -d "{\"hold_id\":\"$HOLD\"}" | json_field id)
say "booking $BOOKING created as pending_payment"
say "retry with the same hold -> $(code "$GW/v1/bookings" -H "Authorization: Bearer $ALICE" -d "{\"hold_id\":\"$HOLD\"}") (idempotent: same booking)"
say "status after the saga: $(wait_status "$BOOKING" "$ALICE" confirmed)"
say "bob reads alice's booking -> $(code "$GW/v1/bookings/$BOOKING" -H "Authorization: Bearer $BOB")"

step "5. A declined card cancels the booking and frees the seats"
DECLINE=$(new_event "$ADMIN" 2513)
SEAT=$(curl -s "$GW/v1/events/$DECLINE/seats" | grep -oE '"id":"[^"]+"' | head -1 | cut -d'"' -f4)
H2=$(curl -s "$GW/v1/holds" -H "Authorization: Bearer $BOB" -d "{\"event_id\":\"$DECLINE\",\"seat_ids\":[\"$SEAT\"]}" | json_field id)
B2=$(curl -s "$GW/v1/bookings" -H "Authorization: Bearer $BOB" -d "{\"hold_id\":\"$H2\"}" | json_field id)
say "price 25.13 is declined by the mock provider -> booking $(wait_status "$B2" "$BOB" cancelled)"
say "availability: $(curl -s "$GW/v1/events/$DECLINE/availability" -H "Authorization: Bearer $BOB")"

step "6. Where to look next"
say "emails sent to alice and bob:   http://localhost:8025"
say "traces, metrics, logs:          http://localhost:3000  (Explore > Tempo, search for booking $BOOKING)"
say "Kafka topics and messages:      http://localhost:8085"
say "gateway routes and middlewares: http://localhost:8090"
