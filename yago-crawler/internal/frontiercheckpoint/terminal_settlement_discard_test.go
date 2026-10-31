package frontiercheckpoint

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/D4rk4/yago/yago-crawler/internal/crawlsettlement"
)

func TestTerminalSettlementDiscardRemovesOnlyTheExactRefusedRow(t *testing.T) {
	checkpoint := openTestCheckpoint(t, testCheckpointPath(t))
	refused := completedTerminalSettlement(t, checkpoint, "lease-refused")
	retained := completedTerminalSettlement(t, checkpoint, "lease-retained")
	for _, settlement := range []crawlsettlement.Settlement{refused, retained} {
		if err := checkpoint.Stage(testContext, settlement); err != nil {
			t.Fatalf("stage terminal settlement %q: %v", settlement.LeaseID, err)
		}
	}
	otherIdentity := bytes.Repeat([]byte{9}, sha256.Size)
	if err := checkpoint.Discard(
		testContext,
		refused.LeaseID,
		otherIdentity,
	); !errors.Is(err, crawlsettlement.ErrDefinitionConflict) {
		t.Fatalf("discard with other order identity = %v, want definition conflict", err)
	}
	if _, found, err := checkpoint.Current(
		testContext,
		refused.LeaseID,
		refused.OrderIdentity,
	); err != nil || !found {
		t.Fatalf("mismatched discard removed row: found=%v err=%v", found, err)
	}
	if err := checkpoint.Discard(testContext, refused.LeaseID, refused.OrderIdentity); err != nil {
		t.Fatalf("discard refused settlement: %v", err)
	}
	if _, found, err := checkpoint.Current(
		testContext,
		refused.LeaseID,
		refused.OrderIdentity,
	); err != nil || found {
		t.Fatalf("discarded row remains: found=%v err=%v", found, err)
	}
	if err := checkpoint.Discard(testContext, refused.LeaseID, refused.OrderIdentity); err != nil {
		t.Fatalf("repeat discard of missing row: %v", err)
	}
	awaiting, err := checkpoint.Awaiting(testContext)
	if err != nil || len(awaiting) != 1 || awaiting[0].LeaseID != retained.LeaseID {
		t.Fatalf("awaiting after discard = %+v, %v", awaiting, err)
	}
}
