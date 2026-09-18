package vm

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Budget reservations are durably charged before forwarding a block. Restart
// discards unused credit, so a crash can reduce allowance but never replenish it.
func durableEgressBudget(directory string, maximum uint64) (func(int) error, error) {
	type state struct {
		Maximum   uint64 `json:"maximum"`
		Remaining uint64 `json:"remaining"`
	}
	saved := state{Maximum: maximum, Remaining: maximum}
	data, err := os.ReadFile(filepath.Join(directory, "egress.json"))
	if err == nil {
		if len(data) > 4096 || strictJSON(data, &saved) != nil || saved.Maximum != maximum || saved.Remaining > maximum {
			return nil, fmt.Errorf("invalid egress budget journal")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	var mu sync.Mutex
	var credit uint64
	return func(n int) error {
		mu.Lock()
		defer mu.Unlock()
		if n < 0 {
			return fmt.Errorf("invalid egress charge")
		}
		if uint64(n) > credit {
			reserve := min(uint64(4<<20), saved.Remaining)
			if reserve < uint64(n)-credit {
				return fmt.Errorf("egress byte budget exhausted")
			}
			saved.Remaining -= reserve
			if err := durableJSON(directory, "egress.json", saved); err != nil {
				return err
			}
			credit += reserve
		}
		credit -= uint64(n)
		return nil
	}, nil
}
