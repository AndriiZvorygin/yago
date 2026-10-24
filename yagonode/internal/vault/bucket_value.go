package vault

import "fmt"

func (t *Txn) ReadBucketValue(name Name, key Key) ([]byte, bool) {
	raw := t.etx.Bucket(name).Get(key)
	if raw == nil {
		return nil, false
	}

	return append([]byte(nil), raw...), true
}

func (t *Txn) PutBucketValue(name Name, key Key, value []byte) error {
	if !t.etx.Writable() {
		return errReadOnly
	}
	bucket := t.etx.Bucket(name)
	existed := bucket.Get(key) != nil
	if err := bucket.Put(key, append([]byte(nil), value...)); err != nil {
		return fmt.Errorf("put %s value: %w", name, err)
	}
	if existed {
		return nil
	}
	if err := adjustLength(t, name, key, 1); err != nil {
		return fmt.Errorf("count %s value: %w", name, err)
	}
	return nil
}
