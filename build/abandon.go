package build

import (
	"context"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"os"
	"path/filepath"
)

// Abandon removes an unpublished build, retaining its allocation on any unknown outcome.
func (r *Runner) Abandon(ctx context.Context, c lc.Command, _ lc.Observer) (lc.Result, error) {
	if r.Provider == nil {
		return lc.Result{}, fmt.Errorf("native cleanup provider is required")
	}
	j := journal.Journal{Root: filepath.Join(r.Root, c.Owner, "validation")}
	var receipt validationRecord
	err := j.Load("receipt", &receipt)
	if err != nil && !os.IsNotExist(err) {
		return lc.Result{}, err
	}
	var attempt lc.Command
	if err = j.Load(validationMachineKey(receipt), &attempt); err == nil && receipt.Machine.ID == "" {
		attempt.Action = "discover"
		result, err := r.Provider.Execute(ctx, attempt, nil)
		if err != nil {
			return lc.Result{}, err
		}
		if result.Absent {
			return lc.Result{}, fmt.Errorf("validation create outcome unresolved; allocation retained")
		}
		receipt.Machine = result.Resource
	} else if err != nil && !os.IsNotExist(err) {
		return lc.Result{}, err
	}
	if receipt.Network.ID != "" || receipt.Machine.ID != "" {
		receipt.ChecksDone = true
		receipt.CheckError = ""
		if err = j.Save("receipt", receipt); err != nil {
			return lc.Result{}, err
		}
		if _, err = finishValidation(ctx, r.Provider, c, lc.Resource{ID: receipt.ImageID}, j, &receipt); err != nil {
			return lc.Result{}, err
		}
	}
	image, found, err := r.Find(ctx, c)
	if err != nil {
		return lc.Result{}, err
	}
	if found {
		result, err := r.Provider.Execute(ctx, lc.Command{Action: "resource.destroy", Operation: c.Owner + "/abandon-image", Owner: c.Owner, Environment: c.Environment, Prior: image}, nil)
		if err != nil {
			return lc.Result{}, err
		}
		if !result.Absent {
			return lc.Result{}, fmt.Errorf("image deletion unconfirmed")
		}
	}
	if err = r.Cleanup(ctx, c); err != nil {
		return lc.Result{}, err
	}
	return lc.Result{Absent: true}, nil
}
