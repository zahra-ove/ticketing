-- Run after a load test against the booking database.
-- :event_id is the event created by the test (printed by k6 as EVENT_ID).

\echo '1. Seats in more than one active booking (must be 0 rows):'
SELECT seat_id, count(*) AS active_bookings
FROM booking_seats
WHERE event_id = :'event_id' AND active
GROUP BY seat_id
HAVING count(*) > 1;

\echo '2. Bookings by status (pending_payment should drain to 0 within seconds):'
SELECT status, coalesce(cancel_reason, '-') AS cancel_reason, count(*)
FROM bookings
WHERE event_id = :'event_id'
GROUP BY status, cancel_reason
ORDER BY status;

\echo '3. Seats sold:'
SELECT count(*) AS seats_in_active_bookings
FROM booking_seats
WHERE event_id = :'event_id' AND active;
