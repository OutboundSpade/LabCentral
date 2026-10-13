package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type cliConfig struct {
	Engine      lc.EngineOptions `json:"engine"`
	State       string           `json:"state"`
	Environment string           `json:"environment"`
	Provider    string           `json:"provider"`
}

func defaultConfigPath() string {
	if path := os.Getenv("LABCENTRAL_CONFIG"); path != "" {
		return path
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "labcentral", "config.json")
}

func loadDefaults(args []string, o *options) error {
	path := defaultConfigPath()
	explicit := os.Getenv("LABCENTRAL_CONFIG") != ""
	for i := 0; i < len(args) && args[i] != "--"; i++ {
		name, value, equal := strings.Cut(args[i], "=")
		if name != "--config" && name != "-config" {
			if takesValue(strings.TrimLeft(name, "-")) && !equal {
				i++
			}
			continue
		}
		explicit = true
		if equal {
			path = value
		} else {
			i++
			if i >= len(args) {
				return fmt.Errorf("--config requires a file")
			}
			path = args[i]
		}
	}
	o.configFile = path
	if path == "" {
		if explicit {
			return fmt.Errorf("--config requires a file")
		}
		return nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) && !explicit {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open CLI config %s: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil {
		return err
	}
	if len(data) > 64<<10 {
		return fmt.Errorf("CLI config %s exceeds 64 KiB", path)
	}
	if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		return fmt.Errorf("CLI config %s must contain one JSON object", path)
	}
	var c cliConfig
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return fmt.Errorf("read CLI config %s: %w", path, err)
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("CLI config %s must contain one JSON object", path)
	}
	for _, entry := range []struct{ name, value string }{{"state", c.State}, {"environment", c.Environment}, {"provider", c.Provider}} {
		if entry.value != "" && !filepath.IsAbs(entry.value) {
			return fmt.Errorf("CLI config %s: %s must be an absolute path", path, entry.name)
		}
	}
	if err := c.Engine.Validate(); err != nil {
		return fmt.Errorf("CLI config %s: %w", path, err)
	}
	o.engine = c.Engine
	if c.State != "" {
		o.root = c.State
	}
	if c.Environment != "" {
		o.env = c.Environment
	}
	if c.Provider != "" {
		o.provider = c.Provider
	}
	return nil
}

func initConfig(path string) error {
	if path == "" {
		return fmt.Errorf("a config path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cliConfig{Engine: lc.EngineOptions{}.Effective()}, "", "  ")
	if err != nil {
		return err
	}
	// Empty defaults preserve current behavior; paths are filled in by the operator.
	return writeNew(path, append(b, '\n'))
}
