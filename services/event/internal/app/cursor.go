package app

import (
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/zahra-ove/ticketing/services/event/internal/domain"
)

// Cursor is the position after which the next page starts. Keyset pagination
// on (starts_at, id) stays fast on deep pages and is stable under inserts,
// unlike OFFSET.
type Cursor struct {
	StartsAt time.Time `json:"s"`
	ID       uuid.UUID `json:"i"`
}

// Encode returns the opaque, URL-safe representation of the cursor.
func (c Cursor) Encode() string {
	b, _ := json.Marshal(c) // cannot fail: the struct has only time and uuid fields
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor parses a cursor produced by Encode. An empty string means
// "start from the beginning" and returns nil.
func DecodeCursor(s string) (*Cursor, error) {
	if s == "" {
		return nil, nil //nolint:nilnil // nil cursor is a valid "first page" value
	}

	invalid := &domain.ValidationError{Fields: map[string]string{"cursor": "is invalid"}}

	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, invalid
	}
	var c Cursor
	if err := json.Unmarshal(b, &c); err != nil || c.ID == uuid.Nil {
		return nil, invalid
	}
	return &c, nil
}
