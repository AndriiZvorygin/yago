package vault

import (
	"context"
	"errors"
	"testing"
)

func newCursorReadFixture(t *testing.T) (*scriptedEngine, *Vault, *Keyspace[string]) {
	t.Helper()
	engine := &scriptedEngine{}
	v, err := New(engine)
	if err != nil {
		t.Fatal(err)
	}
	keyspace, err := RegisterKeyspace(v, "cleanup", internalStringCodec{})
	if err != nil {
		t.Fatal(err)
	}

	return engine, v, keyspace
}

func TestReadCursorDiscardingCorruptReturnsStoredCursor(t *testing.T) {
	engine, v, keyspace := newCursorReadFixture(t)
	cursor, err := ReadCursorDiscardingCorrupt(t.Context(), v, keyspace, Key("cursor"), 3)
	if err != nil || cursor != nil {
		t.Fatalf("absent cursor = %q/%v", cursor, err)
	}
	engine.buckets["cleanup"].values["cursor"] = []byte("row")
	cursor, err = ReadCursorDiscardingCorrupt(t.Context(), v, keyspace, Key("cursor"), 3)
	if err != nil || string(cursor) != "row" {
		t.Fatalf("stored cursor = %q/%v", cursor, err)
	}
}

func TestReadCursorDiscardingCorruptDiscardsOversizedCursor(t *testing.T) {
	engine, v, keyspace := newCursorReadFixture(t)
	engine.buckets["cleanup"].values["cursor"] = []byte("rows")
	engine.buckets["cleanup"].values["kept"] = []byte("rows")
	cursor, err := ReadCursorDiscardingCorrupt(t.Context(), v, keyspace, Key("cursor"), 3)
	if err != nil || cursor != nil {
		t.Fatalf("corrupt cursor = %q/%v", cursor, err)
	}
	if _, found := engine.buckets["cleanup"].values["cursor"]; found {
		t.Fatal("corrupt cursor survived")
	}
	if _, found := engine.buckets["cleanup"].values["kept"]; !found {
		t.Fatal("unrelated value was discarded")
	}
}

func TestReadCursorDiscardingCorruptKeepsCursorOnReadFailure(t *testing.T) {
	engine, v, keyspace := newCursorReadFixture(t)
	engine.buckets["cleanup"].values["cursor"] = []byte("rows")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ReadCursorDiscardingCorrupt(
		ctx, v, keyspace, Key("cursor"), 3,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read = %v", err)
	}
	if _, found := engine.buckets["cleanup"].values["cursor"]; !found {
		t.Fatal("cursor was discarded after a read failure")
	}
}

func TestReadCursorDiscardingCorruptReportsDiscardFailure(t *testing.T) {
	failure := errors.New("delete failed")
	engine, v, keyspace := newCursorReadFixture(t)
	engine.buckets["cleanup"].values["cursor"] = []byte("rows")
	engine.buckets["cleanup"].deleteErr = failure
	if _, err := ReadCursorDiscardingCorrupt(
		t.Context(), v, keyspace, Key("cursor"), 3,
	); !errors.Is(err, failure) {
		t.Fatalf("discard failure = %v, want %v", err, failure)
	}
}
