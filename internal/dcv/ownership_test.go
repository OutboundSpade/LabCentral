package dcv

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestLinuxDCVRevocationOwnershipMatrix(t *testing.T) {
	for _, outcome := range []string{"absent", "owned", "foreign", "missing-owner", "unknown", "response-loss", "unconfirmed"} {
		t.Run(outcome, func(t *testing.T) {
			closed := 0
			run := func(ctx context.Context, command string) ([]byte, error) {
				if strings.Contains(command, "close-session") {
					closed++
					if outcome == "response-loss" {
						return nil, context.DeadlineExceeded
					}
					return nil, nil
				}
				if outcome == "unknown" {
					return nil, nil
				}
				if outcome == "absent" || (closed > 0 && outcome != "unconfirmed") {
					return []byte(`[]`), nil
				}
				owner := "student"
				if outcome == "foreign" {
					owner = "another-owner"
				}
				if outcome == "missing-owner" {
					owner = ""
				}
				return []byte(`[{"id":"student","owner":"` + owner + `","status":"running","x11-display":":1"}]`), nil
			}
			err := Close(context.Background(), run, "student", "student")
			switch outcome {
			case "owned":
				if err != nil || closed != 1 {
					t.Fatal(err, closed)
				}
			case "absent":
				if err != nil || closed != 0 {
					t.Fatal(err, closed)
				}
			case "foreign", "missing-owner":
				if err == nil || !strings.Contains(err.Error(), "ownership_mismatch") || closed != 0 {
					t.Fatal("foreign session touched", err, closed)
				}
			case "unknown":
				if err == nil || closed != 0 {
					t.Fatal("empty status assumed absent", err, closed)
				}
			case "response-loss":
				if !errors.Is(err, context.DeadlineExceeded) || closed != 1 {
					t.Fatal("lost close replayed", err, closed)
				}
			default:
				if err == nil || closed != 1 {
					t.Fatal(err, closed)
				}
			}
		})
	}
}
func TestLinuxDCVReadinessRefusesForeignOwner(t *testing.T) {
	run := func(context.Context, string) ([]byte, error) {
		return []byte(`[{"id":"student","owner":"another-owner","status":"running","x11-display":":1"}]`), nil
	}
	if ready, err := Ready(context.Background(), run, "student"); err == nil || ready {
		t.Fatal("foreign desktop became ready", ready, err)
	}
}
