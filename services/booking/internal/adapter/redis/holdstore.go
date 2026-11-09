// Package redis implements seat holds on Redis.
//
// Data layout, per event (the {evt:<id>} hash tag keeps all keys of an event
// in one Redis Cluster slot, so the Lua scripts can touch them atomically):
//
//	ticketing:{evt:<event>}:seat:<seat>  -> hold id        (string, expires with the hold)
//	ticketing:{evt:<event>}:held         -> seat ids       (sorted set, score = expiry in ms)
//	ticketing:hold:<hold>                -> hold as JSON   (string, expires with the hold)
//
// Seat keys are the lock: SET only succeeds when every seat is free, inside one
// script, so two customers can never hold the same seat. The sorted set only
// serves availability listings.
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/zahra-ove/ticketing/services/booking/internal/app"
	"github.com/zahra-ove/ticketing/services/booking/internal/domain"
)

// holdScript atomically holds every seat or none.
// KEYS[1] = held set, KEYS[2..] = seat keys; ARGV[1] = hold id, ARGV[2] = ttl ms, ARGV[3..] = seat ids.
// Returns the ids of seats that are already held (empty on success).
var holdScript = redis.NewScript(`
local taken = {}
for i = 2, #KEYS do
  if redis.call('EXISTS', KEYS[i]) == 1 then
    taken[#taken + 1] = ARGV[i + 1]
  end
end
if #taken > 0 then
  return taken
end

local t = redis.call('TIME')
local now_ms = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local ttl = tonumber(ARGV[2])
for i = 2, #KEYS do
  redis.call('SET', KEYS[i], ARGV[1], 'PX', ttl)
  redis.call('ZADD', KEYS[1], now_ms + ttl, ARGV[i + 1])
end
if redis.call('PTTL', KEYS[1]) < ttl then
  redis.call('PEXPIRE', KEYS[1], ttl)
end
return taken
`)

// releaseScript frees only the seats that still belong to the hold.
// KEYS[1] = held set, KEYS[2..] = seat keys; ARGV[1] = hold id, ARGV[2..] = seat ids.
var releaseScript = redis.NewScript(`
for i = 2, #KEYS do
  if redis.call('GET', KEYS[i]) == ARGV[1] then
    redis.call('DEL', KEYS[i])
    redis.call('ZREM', KEYS[1], ARGV[i])
  end
end
return 1
`)

// verifyScript returns 1 when every seat key still belongs to the hold.
// KEYS = seat keys; ARGV[1] = hold id.
var verifyScript = redis.NewScript(`
for i = 1, #KEYS do
  if redis.call('GET', KEYS[i]) ~= ARGV[1] then
    return 0
  end
end
return 1
`)

// heldScript drops expired entries and returns the seats still held.
// KEYS[1] = held set.
var heldScript = redis.NewScript(`
local t = redis.call('TIME')
local now_ms = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
return redis.call('ZRANGE', KEYS[1], 0, -1)
`)

// HoldStore implements app.HoldStore.
type HoldStore struct {
	rdb redis.UniversalClient
}

var _ app.HoldStore = (*HoldStore)(nil)

// NewHoldStore returns a HoldStore backed by rdb.
func NewHoldStore(rdb redis.UniversalClient) *HoldStore {
	return &HoldStore{rdb: rdb}
}

func heldKey(eventID uuid.UUID) string {
	return "ticketing:{evt:" + eventID.String() + "}:held"
}

func seatKey(eventID, seatID uuid.UUID) string {
	return "ticketing:{evt:" + eventID.String() + "}:seat:" + seatID.String()
}

func holdKey(holdID uuid.UUID) string {
	return "ticketing:hold:" + holdID.String()
}

// seatKeysAndArgs builds KEYS = [held set, seat keys...] and the seat ids as ARGV tail.
func seatKeysAndArgs(h *domain.Hold) (keys []string, seatArgs []any) {
	keys = make([]string, 0, len(h.Seats)+1)
	keys = append(keys, heldKey(h.EventID))
	seatArgs = make([]any, len(h.Seats))
	for i, s := range h.Seats {
		keys = append(keys, seatKey(h.EventID, s.ID))
		seatArgs[i] = s.ID.String()
	}
	return keys, seatArgs
}

// holdRecord is the JSON stored for a hold.
type holdRecord struct {
	ID         uuid.UUID    `json:"id"`
	EventID    uuid.UUID    `json:"event_id"`
	CustomerID uuid.UUID    `json:"customer_id"`
	Seats      []seatRecord `json:"seats"`
	ExpiresAt  time.Time    `json:"expires_at"`
}

type seatRecord struct {
	ID         uuid.UUID `json:"id"`
	Label      string    `json:"label"`
	PriceCents int64     `json:"price_cents"`
}

// Hold implements app.HoldStore.
func (s *HoldStore) Hold(ctx context.Context, h *domain.Hold, ttl time.Duration) error {
	keys, seatArgs := seatKeysAndArgs(h)
	args := append([]any{h.ID.String(), strconv.FormatInt(ttl.Milliseconds(), 10)}, seatArgs...)

	taken, err := holdScript.Run(ctx, s.rdb, keys, args...).StringSlice()
	if err != nil {
		return fmt.Errorf("run hold script: %w", err)
	}
	if len(taken) > 0 {
		ids := make([]uuid.UUID, 0, len(taken))
		for _, t := range taken {
			if id, err := uuid.Parse(t); err == nil {
				ids = append(ids, id)
			}
		}
		return &domain.SeatsUnavailableError{SeatIDs: ids}
	}

	// The record is written after the seats are locked. If the process dies in
	// between, the seat keys simply expire: nothing is left locked for good.
	rec := holdRecord{ID: h.ID, EventID: h.EventID, CustomerID: h.CustomerID, ExpiresAt: h.ExpiresAt, Seats: make([]seatRecord, len(h.Seats))}
	for i, st := range h.Seats {
		rec.Seats[i] = seatRecord{ID: st.ID, Label: st.Label, PriceCents: st.PriceCents}
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode hold: %w", err)
	}
	if err := s.rdb.Set(ctx, holdKey(h.ID), b, ttl).Err(); err != nil {
		// Undo the seat locks so the seats do not stay blocked without a record.
		_ = s.Release(context.WithoutCancel(ctx), h)
		return fmt.Errorf("store hold: %w", err)
	}
	return nil
}

// Get implements app.HoldStore.
func (s *HoldStore) Get(ctx context.Context, holdID uuid.UUID) (*domain.Hold, error) {
	b, err := s.rdb.Get(ctx, holdKey(holdID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, domain.ErrHoldNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get hold: %w", err)
	}

	var rec holdRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, fmt.Errorf("decode hold: %w", err)
	}
	h := &domain.Hold{ID: rec.ID, EventID: rec.EventID, CustomerID: rec.CustomerID, ExpiresAt: rec.ExpiresAt, Seats: make([]domain.Seat, len(rec.Seats))}
	for i, st := range rec.Seats {
		h.Seats[i] = domain.Seat{ID: st.ID, Label: st.Label, PriceCents: st.PriceCents}
	}
	return h, nil
}

// Verify implements app.HoldStore.
func (s *HoldStore) Verify(ctx context.Context, h *domain.Hold) error {
	keys := make([]string, len(h.Seats))
	for i, st := range h.Seats {
		keys[i] = seatKey(h.EventID, st.ID)
	}
	ok, err := verifyScript.Run(ctx, s.rdb, keys, h.ID.String()).Int()
	if err != nil {
		return fmt.Errorf("run verify script: %w", err)
	}
	if ok != 1 {
		return domain.ErrHoldNotFound
	}
	return nil
}

// Release implements app.HoldStore.
func (s *HoldStore) Release(ctx context.Context, h *domain.Hold) error {
	keys, seatArgs := seatKeysAndArgs(h)
	args := append([]any{h.ID.String()}, seatArgs...)
	if err := releaseScript.Run(ctx, s.rdb, keys, args...).Err(); err != nil {
		return fmt.Errorf("run release script: %w", err)
	}
	if err := s.rdb.Del(ctx, holdKey(h.ID)).Err(); err != nil {
		return fmt.Errorf("delete hold: %w", err)
	}
	return nil
}

// HeldSeats implements app.HoldStore.
func (s *HoldStore) HeldSeats(ctx context.Context, eventID uuid.UUID) ([]uuid.UUID, error) {
	raw, err := heldScript.Run(ctx, s.rdb, []string{heldKey(eventID)}).StringSlice()
	if err != nil {
		return nil, fmt.Errorf("run held script: %w", err)
	}
	ids := make([]uuid.UUID, 0, len(raw))
	for _, r := range raw {
		if id, err := uuid.Parse(r); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
