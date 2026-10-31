package crawlbroker

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func terminalSettlementRefusalStatus(err error) error {
	switch {
	case errors.Is(err, errLeaseDispositionConflict):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, errLeaseLost):
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
