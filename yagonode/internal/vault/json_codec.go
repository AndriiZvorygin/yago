package vault

import (
	"encoding/json"
	"fmt"
)

type JSONCodec[V any] struct{}

func (JSONCodec[V]) Encode(value V) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode JSON: %w", err)
	}

	return raw, nil
}

func (JSONCodec[V]) Decode(raw []byte) (V, error) {
	var value V
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, fmt.Errorf("decode JSON: %w", err)
	}

	return value, nil
}
