package plugin

import (
	"context"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/store"
)

type fencedProvider struct {
	root     string
	provider lc.Provider
}

// WithNativeFences keeps a provider operation fenced across independent provider
// processes, including the interval after its controlling CLI has died. It opens
// only private provider lock files, never the library database.
func WithNativeFences(root string, p lc.Provider) lc.Provider { return &fencedProvider{root, p} }
func independentMachine(c lc.Command) bool {
	return c.Action == "machine.create" || (c.Action == "resource.destroy" && c.Prior.Kind == "machine") || (c.Action == "discover" && c.Machine.Name != "")
}
func (p *fencedProvider) Execute(ctx context.Context, c lc.Command, o lc.Observer) (lc.Result, error) {
	if c.Owner == "" {
		return p.provider.Execute(ctx, c, o)
	}
	shared := independentMachine(c)
	unlock, err := store.LockDirectoryContext(ctx, p.root, "owner:"+c.Owner, shared)
	if err != nil {
		return lc.Result{}, err
	}
	defer unlock()
	if shared {
		name := c.Machine.Name
		if c.Action == "resource.destroy" {
			name = c.Prior.Name
			if name == "" {
				name = c.Prior.ID
			}
		}
		release, err := store.LockDirectoryContext(ctx, p.root, "machine:"+c.Owner+"\x00"+name, false)
		if err != nil {
			return lc.Result{}, err
		}
		defer release()
	}
	return p.provider.Execute(ctx, c, o)
}
