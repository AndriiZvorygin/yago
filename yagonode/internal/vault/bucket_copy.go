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
		return err
	}
	return v.copyBucketPages(ctx, source, name, pageSize)
}

func (v *Vault) copyBucketPages(ctx context.Context, source *Vault, name Name, pageSize int) error {
	var after Key
	for {
		page, err := readBucketCopyPage(ctx, source, name, after, pageSize)
		if err != nil {
			return fmt.Errorf("read source bucket %s: %w", name, err)
		}
		if len(page.Entries) == 0 {
			return nil
		}
		if err := v.writeMissingBucketPage(ctx, name, page.Entries); err != nil {
			return fmt.Errorf("write destination bucket %s: %w", name, err)
		}
		after = append(after[:0], page.Entries[len(page.Entries)-1].Key...)
		if !page.More {
			return nil
		}
	}
}

func readBucketCopyPage(
	ctx context.Context,
	source *Vault,
	name Name,
	after Key,
	pageSize int,
) (BucketPage, error) {
	var page BucketPage
	err := source.View(ctx, func(tx *Txn) error {
		var err error
		page, err = tx.ReadBucketPage(name, after, pageSize)
		return err
	})
	return page, err
}

func (v *Vault) writeMissingBucketPage(
	ctx context.Context,
	name Name,
	entries []BucketPageEntry,
) error {
	return v.Update(ctx, func(tx *Txn) error {
		for _, entry := range entries {
			if _, found := tx.ReadBucketValue(name, entry.Key); found {
				continue
			}
			if err := tx.PutBucketValue(name, entry.Key, entry.Value); err != nil {
				return err
			}
		}
		return nil
	})
}

func (v *Vault) provisionBucket(name Name) error {
	lease, err := v.acquireEngineLease()
	if err != nil {
		return err
	}
	defer lease.release()
	if err := lease.engine.Provision(name); err != nil {
		return fmt.Errorf("provision bucket %s: %w", name, err)
	}
	return nil
}
