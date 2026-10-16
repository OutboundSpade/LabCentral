package build

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
)

type abandonNative struct {
	image, foreign, lose bool
	j                    journal.Journal
}

func (p *abandonNative) Execute(_ context.Context, c lc.Command, _ lc.Observer) (lc.Result, error) {
	var reconciled bool
	if err := p.j.Load("capture-confirmed", &reconciled); err != nil || !reconciled {
		return lc.Result{}, fmt.Errorf("capture deleted before create reconciliation")
	}
	if p.foreign {
		return lc.Result{}, errors.New("ownership_mismatch: image retained")
	}
	p.image = false
	if p.lose {
		p.lose = false
		return lc.Result{}, errors.New("response lost after image deletion")
	}
	return lc.Result{Absent: true}, nil
}
func TestAbandonReconcilesCaptureBeforeDeletion(t *testing.T) {
	for _, fault := range []string{"none", "unknown-capture", "foreign", "response-loss"} {
		t.Run(fault, func(t *testing.T) {
			root := t.TempDir()
			p := &abandonNative{image: true, foreign: fault == "foreign", lose: fault == "response-loss", j: journal.Journal{Root: filepath.Join(root, "native")}}
			c := lc.Command{Owner: "build-example"}
			runner := func() *Runner {
				return &Runner{Provider: p, Root: root, Find: func(context.Context, lc.Command) (lc.Resource, bool, error) {
					return lc.Resource{Kind: "image", Owner: c.Owner, ID: "native-example"}, p.image, nil
				}, Cleanup: func(context.Context, lc.Command) error {
					var done bool
					if p.j.Load("capture-confirmed", &done) == nil && done {
						return nil
					}
					if fault == "unknown-capture" {
						return errors.New("recovery_required: native capture unresolved")
					}
					if !p.image {
						return errors.New("authoritative capture was deleted before reconciliation")
					}
					return p.j.Save("capture-confirmed", true)
				}}
			}
			result, err := runner().Abandon(context.Background(), c, nil)
			if fault == "unknown-capture" || fault == "foreign" {
				if err == nil || !p.image {
					t.Fatal("unconfirmed/foreign capture deleted", result, err)
				}
				if fault == "unknown-capture" && !strings.Contains(err.Error(), "recovery_required") {
					t.Fatal(err)
				}
				return
			}
			if fault == "response-loss" {
				if err == nil || p.image {
					t.Fatal(result, err)
				}
				result, err = runner().Abandon(context.Background(), c, nil)
			}
			if err != nil || !result.Absent || p.image {
				t.Fatal(result, err)
			}
		})
	}
}
