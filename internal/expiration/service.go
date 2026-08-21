package expiration

import (
	"context"
	"fmt"
	"time"

	"go-base/internal/domain"
	"go-base/internal/inventory"
)

type AuditRecorder interface {
	RecordExpiry(context.Context, inventory.Lot) error
	UndoExpiry(context.Context, string) error
}

func Run(ctx context.Context, lots []inventory.Lot, at time.Time, recorder AuditRecorder) error {
	if recorder == nil || at.IsZero() {
		return fmt.Errorf("%w: expiry run dependencies", domain.ErrInvalid)
	}
	for index := range lots {
		updated, expired, err := inventory.Expire(lots[index], at)
		if err != nil {
			return err
		}
		if !expired {
			continue
		}
		lots[index] = updated
		if err := recorder.RecordExpiry(ctx, updated); err != nil {
			return fmt.Errorf("record expiry for lot %s: %w", updated.ID, err)
		}
	}
	return nil
}
