package vault

import "fmt"

func (k *Keyspace[V]) GetWithinSize(tx *Txn, key Key, maximumBytes int) (V, bool, error) {
	var zero V
	size, found, err := k.EncodedSize(tx, key)
	if err != nil {
		return zero, found, fmt.Errorf("inspect %s: %w", k.name, err)
	}
	if !found {
		return zero, false, nil
	}
	if size > maximumBytes {
		return zero, true, fmt.Errorf("%w: %s value size %d", ErrCorruptValue, k.name, size)
	}
	value, present, err := k.Get(tx, key)
	if err != nil {
		return zero, true, err
	}
	if !present {
		return zero, true, fmt.Errorf(
			"%w: %s value disappeared during read", ErrCorruptValue, k.name,
		)
	}

	return value, true, nil
}
