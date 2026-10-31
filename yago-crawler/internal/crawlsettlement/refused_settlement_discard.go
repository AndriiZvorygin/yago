package crawlsettlement

import "context"

type RefusedSettlementDiscarder interface {
	Discard(context.Context, string, []byte) error
}
