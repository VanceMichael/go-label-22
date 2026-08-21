package inventory

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"go-base/internal/domain"
)

type LotStatus string

const (
	LotReceived   LotStatus = "received"
	LotReleased   LotStatus = "released"
	LotQuarantine LotStatus = "quarantine"
	LotExhausted  LotStatus = "exhausted"
	LotExpired    LotStatus = "expired"
)

type Lot struct {
	ID         string
	TenantID   string
	FeedCode   string
	SupplierID string
	QuantityKg float64
	ReservedKg float64
	ConsumedKg float64
	ProducedAt time.Time
	ExpiresAt  time.Time
	ReceivedAt time.Time
	Status     LotStatus
	Version    int64
}

type Reservation struct {
	ID        string
	TenantID  string
	PlanID    string
	FeedCode  string
	Lines     []ReservationLine
	TotalKg   float64
	Status    string
	CreatedAt time.Time
	Version   int64
}

type ReservationLine struct {
	LotID string
	Kg    float64
}

type LedgerEntry struct {
	ID          string
	TenantID    string
	FeedCode    string
	LotID       string
	ReferenceID string
	Kind        string
	QuantityKg  float64
	OccurredAt  time.Time
}

type Balance struct {
	FeedCode    string
	OnHandKg    float64
	ReservedKg  float64
	AvailableKg float64
	ExpiredKg   float64
	LotCount    int
}

func (l Lot) Validate(now time.Time) error {
	if l.ID == "" || l.TenantID == "" || l.FeedCode == "" || l.SupplierID == "" {
		return fmt.Errorf("%w: feed lot identity", domain.ErrInvalid)
	}
	if l.QuantityKg <= 0 || l.ReservedKg < 0 || l.ConsumedKg < 0 {
		return fmt.Errorf("%w: feed lot quantity", domain.ErrInvalid)
	}
	if l.ReservedKg+l.ConsumedKg > l.QuantityKg+0.0001 {
		return fmt.Errorf("%w: feed lot allocation exceeds quantity", domain.ErrConflict)
	}
	if l.ProducedAt.IsZero() || l.ExpiresAt.IsZero() || !l.ExpiresAt.After(l.ProducedAt) {
		return fmt.Errorf("%w: feed lot dates", domain.ErrInvalid)
	}
	if l.ReceivedAt.Before(l.ProducedAt) {
		return fmt.Errorf("%w: feed lot received before production", domain.ErrInvalid)
	}
	if l.Status == LotReleased && !l.ExpiresAt.After(now) {
		return fmt.Errorf("%w: expired lot cannot remain released", domain.ErrConflict)
	}
	return nil
}

func Release(lot Lot, certificateID string, at time.Time) (Lot, LedgerEntry, error) {
	if lot.Status != LotReceived && lot.Status != LotQuarantine {
		return lot, LedgerEntry{}, fmt.Errorf("%w: lot status %s", domain.ErrConflict, lot.Status)
	}
	if strings.TrimSpace(certificateID) == "" {
		return lot, LedgerEntry{}, fmt.Errorf("%w: release certificate", domain.ErrInvalid)
	}
	if !lot.ExpiresAt.After(at) {
		return lot, LedgerEntry{}, fmt.Errorf("%w: lot already expired", domain.ErrConflict)
	}
	out := lot
	out.Status = LotReleased
	out.Version++
	entry := LedgerEntry{ID: "release-" + lot.ID, TenantID: lot.TenantID, FeedCode: lot.FeedCode, LotID: lot.ID, ReferenceID: certificateID, Kind: "release", QuantityKg: lot.QuantityKg, OccurredAt: at}
	return out, entry, nil
}

func Expire(lot Lot, at time.Time) (Lot, bool, error) {
	switch {
	case lot.ID == "" || lot.TenantID == "" || at.IsZero():
		return lot, false, fmt.Errorf("%w: lot expiry request", domain.ErrInvalid)
	case lot.Status == LotExpired || lot.Status == LotExhausted || lot.ExpiresAt.After(at):
		return lot, false, nil
	case lot.ReservedKg > 0:
		return lot, false, fmt.Errorf("%w: expired lot still reserved", domain.ErrConflict)
	}
	out := lot
	out.Status = LotExpired
	out.Version++
	return out, true, nil
}

func Allocate(lots []Lot, tenantID, planID, feedCode string, requiredKg float64, at time.Time) (Reservation, []Lot, error) {
	if tenantID == "" || planID == "" || feedCode == "" || requiredKg <= 0 {
		return Reservation{}, nil, fmt.Errorf("%w: feed allocation request", domain.ErrInvalid)
	}
	eligible := make([]Lot, 0, len(lots))
	for _, lot := range lots {
		if lot.TenantID != tenantID || lot.FeedCode != feedCode || lot.Status != LotReleased || !lot.ExpiresAt.After(at) {
			continue
		}
		eligible = append(eligible, lot)
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		if eligible[i].ExpiresAt.Equal(eligible[j].ExpiresAt) {
			if eligible[i].ReceivedAt.Equal(eligible[j].ReceivedAt) {
				return eligible[i].ID < eligible[j].ID
			}
			return eligible[i].ReceivedAt.Before(eligible[j].ReceivedAt)
		}
		return eligible[i].ExpiresAt.Before(eligible[j].ExpiresAt)
	})
	remaining := requiredKg
	reservation := Reservation{ID: "reservation-" + planID, TenantID: tenantID, PlanID: planID, FeedCode: feedCode, TotalKg: requiredKg, Status: "active", CreatedAt: at, Version: 1}
	updated := append([]Lot(nil), lots...)
	for _, lot := range eligible {
		available := lot.QuantityKg - lot.ReservedKg - lot.ConsumedKg
		if available <= 0 {
			continue
		}
		allocated := available
		if allocated > remaining {
			allocated = remaining
		}
		reservation.Lines = append(reservation.Lines, ReservationLine{LotID: lot.ID, Kg: allocated})
		for i := range updated {
			if updated[i].ID == lot.ID {
				updated[i].ReservedKg += allocated
				updated[i].Version++
				break
			}
		}
		remaining -= allocated
		if remaining <= 0.0001 {
			break
		}
	}
	if remaining > 0.0001 {
		return Reservation{}, nil, fmt.Errorf("%w: %.3f kg feed shortfall", domain.ErrConflict, remaining)
	}
	return reservation, updated, nil
}

func Consume(reservation Reservation, lots []Lot, deliveredKg float64, at time.Time) (Reservation, []Lot, []LedgerEntry, error) {
	if reservation.Status != "active" {
		return reservation, nil, nil, fmt.Errorf("%w: reservation is %s", domain.ErrConflict, reservation.Status)
	}
	if deliveredKg <= 0 || deliveredKg > reservation.TotalKg*1.05 {
		return reservation, nil, nil, fmt.Errorf("%w: delivered feed quantity", domain.ErrInvalid)
	}
	updated := append([]Lot(nil), lots...)
	remaining := deliveredKg
	entries := make([]LedgerEntry, 0, len(reservation.Lines))
	for _, line := range reservation.Lines {
		consume := line.Kg
		if consume > remaining {
			consume = remaining
		}
		found := false
		for i := range updated {
			if updated[i].ID != line.LotID {
				continue
			}
			found = true
			if updated[i].ReservedKg+0.0001 < line.Kg {
				return reservation, nil, nil, fmt.Errorf("%w: reservation no longer backed by lot", domain.ErrConflict)
			}
			updated[i].ReservedKg -= line.Kg
			updated[i].ConsumedKg += consume
			if updated[i].ConsumedKg >= updated[i].QuantityKg-0.0001 {
				updated[i].Status = LotExhausted
			}
			updated[i].Version++
			entries = append(entries, LedgerEntry{ID: fmt.Sprintf("consume-%s-%s", reservation.ID, line.LotID), TenantID: reservation.TenantID, FeedCode: reservation.FeedCode, LotID: line.LotID, ReferenceID: reservation.PlanID, Kind: "consume", QuantityKg: -consume, OccurredAt: at})
			break
		}
		if !found {
			return reservation, nil, nil, fmt.Errorf("%w: reserved lot %s", domain.ErrNotFound, line.LotID)
		}
		remaining -= consume
		if remaining <= 0.0001 {
			break
		}
	}
	if remaining > 0.0001 {
		return reservation, nil, nil, fmt.Errorf("%w: delivered quantity exceeds reserved lots", domain.ErrConflict)
	}
	out := reservation
	out.Status = "consumed"
	out.Version++
	return out, updated, entries, nil
}

func Cancel(reservation Reservation, lots []Lot, at time.Time) (Reservation, []Lot, []LedgerEntry, error) {
	if reservation.Status != "active" {
		return reservation, nil, nil, fmt.Errorf("%w: reservation is %s", domain.ErrConflict, reservation.Status)
	}
	updated := append([]Lot(nil), lots...)
	entries := make([]LedgerEntry, 0, len(reservation.Lines))
	for _, line := range reservation.Lines {
		found := false
		for i := range updated {
			if updated[i].ID != line.LotID {
				continue
			}
			found = true
			if updated[i].ReservedKg+0.0001 < line.Kg {
				return reservation, nil, nil, fmt.Errorf("%w: lot reservation changed", domain.ErrConflict)
			}
			updated[i].ReservedKg -= line.Kg
			updated[i].Version++
			entries = append(entries, LedgerEntry{ID: fmt.Sprintf("release-%s-%s", reservation.ID, line.LotID), TenantID: reservation.TenantID, FeedCode: reservation.FeedCode, LotID: line.LotID, ReferenceID: reservation.PlanID, Kind: "reservation_release", QuantityKg: line.Kg, OccurredAt: at})
			break
		}
		if !found {
			return reservation, nil, nil, fmt.Errorf("%w: lot %s", domain.ErrNotFound, line.LotID)
		}
	}
	out := reservation
	out.Status = "cancelled"
	out.Version++
	return out, updated, entries, nil
}

func Summarize(lots []Lot, tenantID, feedCode string, now time.Time) Balance {
	result := Balance{FeedCode: feedCode}
	for _, lot := range lots {
		if lot.TenantID != tenantID || lot.FeedCode != feedCode {
			continue
		}
		result.LotCount++
		onHand := lot.QuantityKg - lot.ConsumedKg
		result.OnHandKg += onHand
		result.ReservedKg += lot.ReservedKg
		if !lot.ExpiresAt.After(now) || lot.Status == LotExpired {
			result.ExpiredKg += onHand
			continue
		}
		if lot.Status == LotReleased {
			result.AvailableKg += onHand - lot.ReservedKg
		}
	}
	return result
}
