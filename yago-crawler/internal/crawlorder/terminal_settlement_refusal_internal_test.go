package crawlorder

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	grpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/D4rk4/yago/yago-crawler/internal/crawlsettlement"
	"github.com/D4rk4/yago/yago-crawler/internal/frontiercheckpoint"
	"github.com/D4rk4/yago/yagocrawlcontract/crawlrpc"
)

type refusingTerminalClient struct {
	terminalAcknowledgmentClient
	acknowledgmentCode codes.Code
	confirmationCode   codes.Code
	callMutex          sync.Mutex
	acknowledgmentRPCs int
	confirmationRPCs   int
}

func (client *refusingTerminalClient) AckOrder(
	_ context.Context,
	request *crawlrpc.OrderAck,
	_ ...grpc.CallOption,
) (*crawlrpc.OrderAckResult, error) {
	client.callMutex.Lock()
	defer client.callMutex.Unlock()
	if len(request.GetConfirmationToken()) != 0 {
		client.confirmationRPCs++
		if client.confirmationCode != codes.OK {
			return nil, status.Error(client.confirmationCode, "confirmation refused")
		}

		return &crawlrpc.OrderAckResult{}, nil
	}
	client.acknowledgmentRPCs++
	if client.acknowledgmentCode != codes.OK {
		return nil, status.Error(client.acknowledgmentCode, "acknowledgment refused")
	}

	return &crawlrpc.OrderAckResult{
		ConfirmationToken: bytes.Repeat([]byte{7}, sha256.Size),
	}, nil
}

func (client *refusingTerminalClient) rpcs() (int, int) {
	client.callMutex.Lock()
	defer client.callMutex.Unlock()

	return client.acknowledgmentRPCs, client.confirmationRPCs
}

func refusalRelayCheckpoint(t *testing.T) *frontiercheckpoint.FrontierCheckpoint {
	t.Helper()
	checkpoint, err := frontiercheckpoint.Open(filepath.Join(t.TempDir(), "frontier.db"))
	if err != nil {
		t.Fatalf("open refusal checkpoint: %v", err)
	}
	t.Cleanup(func() { _ = checkpoint.Close() })

	return checkpoint
}

func TestTerminalSettlementRefusedByNodeIsDiscardedAndNotRetried(t *testing.T) {
	for _, test := range []struct {
		name                  string
		client                *refusingTerminalClient
		wantAcknowledgments   int
		wantConfirmationCalls int
	}{
		{
			name:                "acknowledgment",
			client:              &refusingTerminalClient{acknowledgmentCode: codes.NotFound},
			wantAcknowledgments: 1,
		},
		{
			name:                  "confirmation",
			client:                &refusingTerminalClient{confirmationCode: codes.NotFound},
			wantAcknowledgments:   1,
			wantConfirmationCalls: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			checkpoint := refusalRelayCheckpoint(t)
			stagedTerminalRelaySettlement(t, checkpoint, "lease-refused", 3)
			relay := newTerminalSettlementRelay(test.client, checkpoint)
			for attempt := range 2 {
				if err := relay.reconcile(t.Context()); err != nil {
					t.Fatalf("reconcile %d after refusal: %v", attempt, err)
				}
			}
			awaiting, err := checkpoint.Awaiting(t.Context())
			if err != nil || len(awaiting) != 0 {
				t.Fatalf("outbox after refusal = %+v, %v", awaiting, err)
			}
			acknowledgments, confirmations := test.client.rpcs()
			if acknowledgments != test.wantAcknowledgments ||
				confirmations != test.wantConfirmationCalls {
				t.Fatalf(
					"refused settlement RPCs = %d acknowledgments, %d confirmations",
					acknowledgments,
					confirmations,
				)
			}
		})
	}
}

func TestTerminalSettlementLeaseLossKeepsTheRowForRetry(t *testing.T) {
	checkpoint := refusalRelayCheckpoint(t)
	staged := stagedTerminalRelaySettlement(t, checkpoint, "lease-lost", 4)
	client := &refusingTerminalClient{acknowledgmentCode: codes.FailedPrecondition}
	relay := newTerminalSettlementRelay(client, checkpoint)
	for attempt := range 2 {
		if err := relay.reconcile(t.Context()); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("reconcile %d after lease loss = %v", attempt, err)
		}
	}
	awaiting, err := checkpoint.Awaiting(t.Context())
	if err != nil || len(awaiting) != 1 || awaiting[0].LeaseID != staged.LeaseID {
		t.Fatalf("outbox after lease loss = %+v, %v", awaiting, err)
	}
	if acknowledgments, _ := client.rpcs(); acknowledgments != 2 {
		t.Fatalf("lease-loss acknowledgments = %d, want 2", acknowledgments)
	}
}

func TestTerminalSettlementRefusalReportsDiscardFailure(t *testing.T) {
	definition := terminalSettlementScenarioDefinition()
	definition.Phase = crawlsettlement.AwaitingAcknowledgment
	fault := errors.New("terminal discard fault")
	outbox := &terminalSettlementOutboxScenario{settlement: definition, discardError: fault}
	relay := newTerminalSettlementRelay(&terminalAcknowledgmentClient{}, outbox)
	err := relay.advanceWithDelivery(
		t.Context(),
		definition,
		func(
			context.Context,
			crawlsettlement.Settlement,
			[]byte,
		) (*crawlrpc.OrderAckResult, error) {
			return nil, status.Error(codes.NotFound, "refused")
		},
	)
	if !errors.Is(err, fault) || status.Code(err) != codes.NotFound {
		t.Fatalf("refusal with discard fault = %v", err)
	}
}
