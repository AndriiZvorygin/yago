package vault

import (
	"context"
	"errors"
	"fmt"
)

const maximumBucketCopyPageSize = 4096

func (v *Vault) CopyBucketFrom(
	ctx context.Context,
	source *Vault,
	name Name,
	pageSize int,
) error {
	if v == nil || source == nil {
		return errors.New("source and destination vaults are required")
	}
	if v == source {
		return nil
	}
	if pageSize < 1 || pageSize > maximumBucketCopyPageSize {
		return fmt.Errorf(
			"bucket copy page size must be between 1 and %d",
			maximumBucketCopyPageSize,
		)
	}
	if err := v.provisionBucket(name); err != nil {
		return fmt.Errorf("provision destination bucket %s: %w", name, err)
	}

	var after Key
	for {
		var page BucketPage
		if err := source.View(ctx, func(tx *Txn) error {
			var err error
			page, err = tx.ReadBucketPage(name, after, pageSize)
			return err
		}); err != nil {
			return fmt.Errorf("read source bucket %s: %w", name, err)
		}
		if len(page.Entries) == 0 {
			return nil
		}
		if err := v.Update(ctx, func(tx *Txn) error {
			for _, entry := range page.Entries {
				if _, found := tx.ReadBucketValue(name, entry.Key); found {
					continue
				}
				if err := tx.PutBucketValue(name, entry.Key, entry.Value); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return fmt.Errorf("write destination bucket %s: %w", name, err)
		}
		after = append(after[:0], page.Entries[len(page.Entries)-1].Key...)
		if !page.More {
			return nil
		}
	}
}

func (v *Vault) provisionBucket(name Name) error {
	lease, err := v.acquireEngineLease()
	if err != nil {
		return err
	}
	defer lease.release()
	return lease.engine.Provision(name)
}
