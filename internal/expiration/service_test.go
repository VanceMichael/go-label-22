package expiration

import (
	"context"
	"errors"
	"testing"
	"time"

	"go-base/internal/inventory"
)

var errExpiryAudit = errors.New("expiry audit unavailable")

type expiryAudit struct {
	failOn   string
	recorded map[string]inventory.Lot
}

func (audit *expiryAudit) RecordExpiry(_ context.Context, lot inventory.Lot) error {
	if lot.ID == audit.failOn {
		return errExpiryAudit
	}
	audit.recorded[lot.ID] = lot
	return nil
}

func (audit *expiryAudit) UndoExpiry(_ context.Context, lotID string) error {
	delete(audit.recorded, lotID)
	return nil
}

func expiringLot(id string, at time.Time) inventory.Lot {
	return inventory.Lot{ID: id, TenantID: "tenant-1", FeedCode: "TMR", SupplierID: "supplier-1", QuantityKg: 50, ProducedAt: at.Add(-48 * time.Hour), ExpiresAt: at.Add(-time.Hour), ReceivedAt: at.Add(-24 * time.Hour), Status: inventory.LotReleased, Version: 3}
}

func TestExpiryAuditFailureRestoresEveryLotAndPriorAudit(t *testing.T) {
	at := time.Now().UTC()
	lots := []inventory.Lot{expiringLot("lot-a", at), expiringLot("lot-b", at)}
	original := append([]inventory.Lot(nil), lots...)
	audit := &expiryAudit{failOn: "lot-b", recorded: map[string]inventory.Lot{}}
	err := Run(context.Background(), lots, at, audit)
	if !errors.Is(err, errExpiryAudit) {
		t.Fatalf("expiry error = %v", err)
	}
	if lots[0] != original[0] || lots[1] != original[1] || len(audit.recorded) != 0 {
		t.Fatalf("failed run retained state: lots=%+v audit=%+v", lots, audit.recorded)
	}

	successLots := append([]inventory.Lot(nil), original...)
	successAudit := &expiryAudit{recorded: map[string]inventory.Lot{}}
	if err := Run(context.Background(), successLots, at, successAudit); err != nil {
		t.Fatalf("successful expiry error = %v", err)
	}
	for _, lot := range successLots {
		if lot.Status != inventory.LotExpired || lot.Version != 4 {
			t.Fatalf("expired lot = %+v", lot)
		}
	}
	if len(successAudit.recorded) != 2 {
		t.Fatalf("expiry audit = %+v", successAudit.recorded)
	}
}
