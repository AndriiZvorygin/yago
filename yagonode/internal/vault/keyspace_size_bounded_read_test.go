package vault

import (
	"errors"
	"testing"
)

func TestKeyspaceGetWithinSizeAdmitsValuesUpToTheBound(t *testing.T) {
	data := &scriptedBucket{values: map[string][]byte{"key": []byte("value")}}
	keyspace, tx := newScriptedKeyspace(data, false, internalStringCodec{})
	value, found, err := keyspace.GetWithinSize(tx, Key("key"), len("value"))
	if err != nil || !found || value != "value" {
		t.Fatalf("bounded value = %q/%t/%v", value, found, err)
	}
	value, found, err = keyspace.GetWithinSize(tx, Key("missing"), len("value"))
	if err != nil || found || value != "" {
		t.Fatalf("missing bounded value = %q/%t/%v", value, found, err)
	}
}

func TestKeyspaceGetWithinSizeRefusesOversizedValuesAsCorrupt(t *testing.T) {
	data := &scriptedBucket{values: map[string][]byte{"key": []byte("value")}}
	keyspace, tx := newScriptedKeyspace(data, false, internalStringCodec{})
	value, found, err := keyspace.GetWithinSize(tx, Key("key"), len("value")-1)
	if !errors.Is(err, ErrCorruptValue) || !found || value != "" {
		t.Fatalf("oversized value = %q/%t/%v", value, found, err)
	}
}

func TestKeyspaceGetWithinSizeReportsStorageFailures(t *testing.T) {
	measurement := errors.New("measure failed")
	sized := &sizedBucket{
		scriptedBucket: &scriptedBucket{values: map[string][]byte{}},
		size:           1,
		found:          true,
		err:            measurement,
	}
	keyspace := &Keyspace[string]{name: "data", codec: internalStringCodec{}}
	tx := &Txn{etx: presenceTxn{bucket: sized}}
	_, found, err := keyspace.GetWithinSize(tx, Key("key"), 1)
	if !errors.Is(err, measurement) || !found {
		t.Fatalf("measurement failure = %t/%v", found, err)
	}
	sized.found = false
	_, found, err = keyspace.GetWithinSize(tx, Key("key"), 1)
	if !errors.Is(err, measurement) || found {
		t.Fatalf("absent measurement failure = %t/%v", found, err)
	}
	sized.err = nil
	sized.found = true
	_, found, err = keyspace.GetWithinSize(tx, Key("key"), 1)
	if !errors.Is(err, ErrCorruptValue) || !found {
		t.Fatalf("disappeared value = %t/%v", found, err)
	}
	decoding := errors.New("decode failed")
	sized.values["key"] = []byte("v")
	keyspace.codec = failingKeyspaceCodec{decode: decoding}
	_, found, err = keyspace.GetWithinSize(tx, Key("key"), 1)
	if !errors.Is(err, decoding) || !found {
		t.Fatalf("decode failure = %t/%v", found, err)
	}
}
