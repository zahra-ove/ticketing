#!/bin/sh
# Creates one database and one owner role per service.
# Runs only once, when the Postgres data volume is empty.
set -eu

create_service_db() {
	name="$1"
	password="$2"

	echo "creating database and role: ${name}"
	psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres <<EOSQL
CREATE ROLE ${name} WITH LOGIN PASSWORD '${password}';
CREATE DATABASE ${name} OWNER ${name};
REVOKE ALL ON DATABASE ${name} FROM PUBLIC;
EOSQL
}

create_service_db event "$EVENT_DB_PASSWORD"
create_service_db booking "$BOOKING_DB_PASSWORD"
create_service_db payment "$PAYMENT_DB_PASSWORD"
