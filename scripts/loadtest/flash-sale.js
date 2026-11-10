// Flash-sale load test: many customers race for the seats of one event.
//
// It answers the questions that matter for a ticketing system:
//   - Is any seat ever sold twice? (checked after the run, in the database)
//   - How fast are holds and bookings under contention?
//   - Does the saga keep up (every booking ends confirmed or cancelled)?
//
// Run it with `make load-test` (50 users) or `make load-test VUS=200`. The test goes through the gateway, with a
// real token per virtual user, exactly like a client would.
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';

// Inclusive random integer; kept local so the test needs no network access.
function randomIntBetween(min, max) {
  return Math.floor(Math.random() * (max - min + 1)) + min;
}

const BASE = __ENV.BASE_URL || 'http://localhost:8000';
const VUS = Number(__ENV.VUS || 50);
const ROWS = Number(__ENV.ROWS || 10);
const SEATS_PER_ROW = Number(__ENV.SEATS_PER_ROW || 20);

// 409 (seat taken) and 429 (rate limited) are expected outcomes in a flash
// sale, not failures of the system.
http.setResponseCallback(http.expectedStatuses(200, 201, 204, 409, 429));

const holdsOK = new Counter('holds_ok');
const holdsTaken = new Counter('holds_seat_taken');
const rateLimited = new Counter('rate_limited');
const bookingsOK = new Counter('bookings_created');
const holdLatency = new Trend('hold_latency', true);
const bookingLatency = new Trend('booking_latency', true);

export const options = {
  // Logging in every customer (paced below the gateway's login rate limit) takes a while.
  setupTimeout: '3m',
  scenarios: {
    flash_sale: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '10s', target: VUS },
        { duration: '30s', target: VUS },
        { duration: '5s', target: 0 },
      ],
    },
  },
  // Budgets for a laptop running the whole stack (see docs/load-test.md).
  // Correctness is checked separately: no seat may be sold twice.
  thresholds: {
    http_req_failed: ['rate<0.01'], // real errors (5xx, timeouts) below 1%
    hold_latency: ['p(95)<1500'],
    booking_latency: ['p(95)<1500'],
  },
};

function login(username, password) {
  const res = http.post(`${BASE}/v1/token`, JSON.stringify({ username, password }), {
    headers: { 'Content-Type': 'application/json' },
    tags: { name: 'login' },
  });
  check(res, { 'login ok': (r) => r.status === 200 });
  return res.json('access_token');
}

// setup runs once: an admin creates and publishes the event.
export function setup() {
  const admin = login('admin', __ENV.ADMIN_PASSWORD || 'admin-secret');
  const auth = { headers: { Authorization: `Bearer ${admin}`, 'Content-Type': 'application/json' } };

  const ev = http.post(`${BASE}/v1/events`, JSON.stringify({
    title: `Flash sale ${Date.now()}`,
    venue: 'Load test arena',
    starts_at: new Date(Date.now() + 30 * 24 * 3600 * 1000).toISOString(),
    sections: [{ name: 'GA', rows: ROWS, seats_per_row: SEATS_PER_ROW, price_cents: 4900 }],
  }), auth);
  check(ev, { 'event created': (r) => r.status === 201 });
  const eventId = ev.json('id');

  http.post(`${BASE}/v1/events/${eventId}/publish`, null, auth);
  const seats = http.get(`${BASE}/v1/events/${eventId}/seats`).json('data').map((s) => s.id);

  // One customer account per virtual user. The gateway allows 5 logins per
  // second per IP and the whole test runs from one IP, so log everyone in
  // here, paced under that limit, instead of in a burst when the test starts.
  const tokens = [];
  for (let vu = 1; vu <= VUS; vu++) {
    tokens.push(login(`customer-loadtest-${vu}`, __ENV.DEMO_PASSWORD || 'demo-secret'));
    sleep(0.21);
  }
  return { eventId, seats, tokens };
}

export default function (data) {
  const params = {
    headers: { Authorization: `Bearer ${data.tokens[(__VU - 1) % data.tokens.length]}`, 'Content-Type': 'application/json' },
  };

  // Pick one or two neighbouring seats, like a customer clicking on a seat map.
  const i = randomIntBetween(0, data.seats.length - 2);
  const wanted = data.seats.slice(i, i + randomIntBetween(1, 2));

  const hold = http.post(`${BASE}/v1/holds`, JSON.stringify({ event_id: data.eventId, seat_ids: wanted }),
    Object.assign({ tags: { name: 'hold' } }, params));
  holdLatency.add(hold.timings.duration);

  if (hold.status === 429) {
    rateLimited.add(1);
    sleep(1);
    return;
  }
  if (hold.status === 409) {
    holdsTaken.add(1);
    sleep(randomIntBetween(1, 3) / 10);
    return;
  }
  if (!check(hold, { 'hold created': (r) => r.status === 201 })) {
    return;
  }
  holdsOK.add(1);

  const booking = http.post(`${BASE}/v1/bookings`, JSON.stringify({ hold_id: hold.json('id') }),
    Object.assign({ tags: { name: 'booking' } }, params));
  bookingLatency.add(booking.timings.duration);
  if (check(booking, { 'booking created': (r) => r.status === 201 })) {
    bookingsOK.add(1);
  }
  sleep(randomIntBetween(2, 5) / 10);
}

export function teardown(data) {
  console.log(`EVENT_ID=${data.eventId} SEATS=${data.seats.length}`);
}
