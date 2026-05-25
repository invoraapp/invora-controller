package controller

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc/connectivity"
)

// checkGatewayConnectivity dials the billing gateway and waits until the gRPC channel is ready.
func checkGatewayConnectivity(ctx context.Context, gatewayURL string) error {
	conn, err := dialGateway(gatewayURL)
	if err != nil {
		return err
	}
	defer conn.Close()

	waitCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
	}

	conn.Connect()
	for {
		state := conn.GetState()
		if state == connectivity.Ready {
			return nil
		}
		if state == connectivity.Shutdown {
			return fmt.Errorf("gateway connection shut down")
		}
		if waitCtx.Err() != nil {
			return fmt.Errorf("waiting for gateway: %w", waitCtx.Err())
		}
		if !conn.WaitForStateChange(waitCtx, state) {
			return fmt.Errorf("waiting for gateway: %w", waitCtx.Err())
		}
	}
}
