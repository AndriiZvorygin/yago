package vault

import (
	"math"
	"testing"
)

type jsonCodecRecord struct {
	Name  string
	Count int
}

func TestJSONCodecRoundTripsARecord(t *testing.T) {
	codec := JSONCodec[jsonCodecRecord]{}
	raw, err := codec.Encode(jsonCodecRecord{Name: "alpha", Count: 3})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := codec.Decode(raw)
	if err != nil || decoded != (jsonCodecRecord{Name: "alpha", Count: 3}) {
		t.Fatalf("decoded = %#v, %v", decoded, err)
	}
}

func TestJSONCodecRefusesValuesJSONCannotCarry(t *testing.T) {
	if raw, err := (JSONCodec[float64]{}).Encode(math.NaN()); err == nil {
		t.Fatalf("encoded NaN as %q", raw)
	}
	if decoded, err := (JSONCodec[jsonCodecRecord]{}).Decode([]byte("{")); err == nil {
		t.Fatalf("decoded truncated JSON as %#v", decoded)
	}
}
