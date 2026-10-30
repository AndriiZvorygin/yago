package vault

import (
	"errors"
	"testing"
)

func TestDeleteKeyRemovesOneKeyInItsOwnTransaction(t *testing.T) {
	engine := &scriptedEngine{}
	v, err := New(engine)
	if err != nil {
		t.Fatal(err)
	}
	keyspace, err := RegisterKeyspace(v, "data", internalStringCodec{})
	if err != nil {
		t.Fatal(err)
	}
	engine.buckets["data"].values["key"] = []byte("value")
	engine.buckets["data"].values["kept"] = []byte("value")
	if err := DeleteKey(t.Context(), v, keyspace, Key("key")); err != nil {
		t.Fatal(err)
	}
	if _, found := engine.buckets["data"].values["key"]; found {
		t.Fatal("deleted key survived")
	}
	if _, found := engine.buckets["data"].values["kept"]; !found {
		t.Fatal("unrelated key was deleted")
	}
	if err := DeleteKey(t.Context(), v, keyspace, Key("missing")); err != nil {
		t.Fatalf("missing key deletion = %v", err)
	}
}

func TestDeleteKeyReportsDeletionFailure(t *testing.T) {
	failure := errors.New("delete failed")
	engine := &scriptedEngine{}
	v, err := New(engine)
	if err != nil {
		t.Fatal(err)
	}
	keyspace, err := RegisterKeyspace(v, "data", internalStringCodec{})
	if err != nil {
		t.Fatal(err)
	}
	engine.buckets["data"].values["key"] = []byte("value")
	engine.buckets["data"].deleteErr = failure
	if err := DeleteKey(t.Context(), v, keyspace, Key("key")); !errors.Is(err, failure) {
		t.Fatalf("deletion failure = %v, want %v", err, failure)
	}
}
