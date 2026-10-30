package vault

import (
	"context"
	"errors"
	"fmt"
)

func ReadCursorDiscardingCorrupt(
	ctx context.Context,
	v *Vault,
	keyspace *Keyspace[string],
	key Key,
	maximumBytes int,
) (Key, error) {
	var cursor Key
	err := v.View(ctx, func(tx *Txn) error {
		value, found, err := keyspace.GetWithinSize(tx, key, maximumBytes)
		if err != nil {
			return err
		}
		if found {
			cursor = Key(value)
		}

		return nil
	})
	if err != nil {
		if !errors.Is(err, ErrCorruptValue) {
			return nil, fmt.Errorf("read %s cursor: %w", keyspace.name, err)
		}
		if err := DeleteKey(ctx, v, keyspace, key); err != nil {
			return nil, fmt.Errorf("discard corrupt %s cursor: %w", keyspace.name, err)
		}
	}

	return cursor, nil
}
