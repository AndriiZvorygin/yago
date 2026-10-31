package crawlorder

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/D4rk4/yago/yago-crawler/internal/crawlsettlement"
)

const msgTerminalSettlementRefused = "terminal crawl settlement refused by node; discarded"

func (relay *terminalSettlementRelay) discardRefusedSettlement(
	ctx context.Context,
	settlement crawlsettlement.Settlement,
	err error,
) error {
	if status.Code(err) != codes.NotFound {
		return err
	}
	if discardErr := relay.outbox.Discard(
		ctx,
		settlement.LeaseID,
		settlement.OrderIdentity,
	); discardErr != nil {
		return errors.Join(
			err,
			fmt.Errorf("discard refused terminal crawl settlement: %w", discardErr),
		)
	}
	slog.WarnContext(
		ctx,
		msgTerminalSettlementRefused,
		slog.String("leaseId", settlement.LeaseID),
		slog.Any("error", err),
	)

	return nil
}
