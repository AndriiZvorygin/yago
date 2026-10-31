package frontiercheckpoint

import (
	"context"

	bolt "go.etcd.io/bbolt"
)

func (checkpoint *FrontierCheckpoint) Discard(
	ctx context.Context,
	leaseID string,
	orderIdentity []byte,
) error {
	return checkpoint.writeTransaction(ctx, func(transaction *bolt.Tx) error {
		outbox, _, found, err := readTerminalSettlement(transaction, leaseID, orderIdentity)
		if err != nil || !found {
			return err
		}

		return deleteRow(outbox, []byte(leaseID), "terminal settlement")
	})
}
