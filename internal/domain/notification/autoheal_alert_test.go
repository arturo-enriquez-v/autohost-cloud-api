// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package notification

import (
	"context"
	"testing"
)

func TestAutoHealTransitionAlert_NoDuplicates(t *testing.T) {
	svc := &Service{} // nil redis is fine for testing transition logic without crashing

	// Case 1: Recovery when OldState was COOLING_DOWN and NewState is HEALTHY
	ctx1 := AutoHealAlertContext{
		Recipient:   "user@example.com",
		ServiceName: "autohost-ollama",
		OldState: AutoHealState{
			Status:            "COOLING_DOWN",
			RestartTimestamps: []int64{1724626600},
		},
		NewState: AutoHealState{
			Status:            "HEALTHY",
			RestartTimestamps: []int64{1724626600},
		},
	}
	svc.SendAutoHealTransitionAlert(context.Background(), ctx1)

	// Case 2: Subsequent check 10 seconds later: OldState is HEALTHY, NewState is HEALTHY
	// (Even if OldState still had timestamps) -> must NOT re-trigger recovery alert!
	ctx2 := AutoHealAlertContext{
		Recipient:   "user@example.com",
		ServiceName: "autohost-ollama",
		OldState: AutoHealState{
			Status:            "HEALTHY",
			RestartTimestamps: []int64{1724626600},
		},
		NewState: AutoHealState{
			Status:            "HEALTHY",
			RestartTimestamps: []int64{},
		},
	}
	svc.SendAutoHealTransitionAlert(context.Background(), ctx2)

	// Case 3: Steady state HEALTHY -> HEALTHY
	ctx3 := AutoHealAlertContext{
		Recipient:   "user@example.com",
		ServiceName: "autohost-ollama",
		OldState: AutoHealState{
			Status:            "HEALTHY",
			RestartTimestamps: []int64{},
		},
		NewState: AutoHealState{
			Status:            "HEALTHY",
			RestartTimestamps: []int64{},
		},
	}
	svc.SendAutoHealTransitionAlert(context.Background(), ctx3)
}
