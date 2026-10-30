package vault

import "context"

func DeleteKey[V any](ctx context.Context, v *Vault, keyspace *Keyspace[V], key Key) error {
	return v.Update(ctx, func(tx *Txn) error {
		_, err := keyspace.Delete(tx, key)

		return err
	})
}
