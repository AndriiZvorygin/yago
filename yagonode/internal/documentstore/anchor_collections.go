package documentstore

import (
	"fmt"

	"github.com/D4rk4/yago/yagonode/internal/vault"
)

const (
	inboundAnchorBucket             vault.Name = "document_inbound_anchors"
	outboundTargetBucket            vault.Name = "document_outbound_targets"
	outboundAnchorPublicationBucket vault.Name = "document_outbound_anchor_publications"
)

type anchorJSONCodec[V any] = vault.JSONCodec[V]

func registerAnchorCollections(
	v *vault.Vault,
) (
	*vault.Keyspace[[]AnchorText],
	*vault.Keyspace[[]string],
	*vault.Keyspace[outboundAnchorPublication],
	error,
) {
	inbound, err := vault.RegisterKeyspace(
		v,
		inboundAnchorBucket,
		anchorJSONCodec[[]AnchorText]{},
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("register inbound anchors: %w", err)
	}
	outbound, err := vault.RegisterKeyspace(
		v,
		outboundTargetBucket,
		anchorJSONCodec[[]string]{},
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("register outbound targets: %w", err)
	}
	publications, err := vault.RegisterKeyspace(
		v,
		outboundAnchorPublicationBucket,
		anchorJSONCodec[outboundAnchorPublication]{},
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("register outbound anchor publications: %w", err)
	}

	return inbound, outbound, publications, nil
}
