package peermessage

import (
	"fmt"

	"github.com/D4rk4/yago/yagonode/internal/vault"
)

const messagesBucket vault.Name = "peermessage"

type messageCodec = vault.JSONCodec[Message]

func registerMessages(v *vault.Vault) (*vault.Collection[Message], error) {
	messages, err := vault.Register(v, messagesBucket, messageCodec{})
	if err != nil {
		return nil, fmt.Errorf("register peer messages: %w", err)
	}

	return messages, nil
}
