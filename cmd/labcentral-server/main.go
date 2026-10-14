package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/hosted"
	"github.com/OutboundSpade/LabCentral/v2/plugin"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	config := flag.String("config", "", "protected JSON configuration file")
	listen := flag.String("listen", "127.0.0.1:8080", "HTTP address behind the TLS proxy")
	provider := flag.String("provider", "", "provider executable")
	maintenance := flag.String("maintenance", "", "local status, recover, or cleanup operation")
	checkConfig := flag.Bool("check-config", false, "validate static configuration and print effective worker limits without starting providers or OIDC")
	flag.Parse()
	if *checkConfig && *maintenance != "" {
		return fmt.Errorf("--check-config cannot be combined with --maintenance")
	}
	if *config == "" {
		return fmt.Errorf("--config is required")
	}
	f, e := os.Open(*config)
	if e != nil {
		return e
	}
	defer f.Close()
	var c hosted.Config
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if e = d.Decode(&c); e != nil {
		return e
	}
	if e = d.Decode(new(any)); e != io.EOF {
		return fmt.Errorf("configuration must contain one JSON object")
	}
	if *maintenance != "" {
		result, e := hosted.Maintain(context.Background(), c.Root, *maintenance)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	if e = hosted.ValidateConfig(c); e != nil {
		return e
	}
	if *checkConfig {
		env, err := lc.LoadEnvironment(c.Environment)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			Aggregate          hosted.AggregateOptions `json:"aggregate"`
			Engine             lc.EngineOptions        `json:"engine"`
			Worker             hosted.WorkerOptions    `json:"worker"`
			WeeklyCloudMinutes int                     `json:"weeklyCloudMinutes"`
			Note               string                  `json:"note"`
			Gateway            lc.AWSGatewayConfig     `json:"awsGateway"`
			AllowEgress        bool                    `json:"allowEgress"`
		}{c.Aggregate.Effective(), c.Engine.Effective(), c.Worker.Effective(), func() int {
			if c.WeeklyCloudMinutes == 0 {
				return 180
			}
			return c.WeeklyCloudMinutes
		}(), "Static policy and environment validated; identity and native capacity were not checked.", env.AWS.Gateway.Effective(), env.AWS.AllowEgress})
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *provider == "" {
		exe, e := os.Executable()
		if e != nil {
			return e
		}
		*provider = filepath.Join(filepath.Dir(exe), "labcentral-provider")
	}
	// The provider outlives HTTP request cancellation and shuts down after access cleanup.
	p, e := plugin.Start(context.Background(), *provider, filepath.Join(c.Root, "engine", "providers"))
	if e != nil {
		return e
	}
	defer p.Close()
	budgeted, e := hosted.NewBudgetedProvider(p, c.Aggregate)
	if e != nil {
		return e
	}
	engine, e := lc.OpenWithOptions(filepath.Join(c.Root, "engine"), budgeted, nil, c.Engine)
	if e != nil {
		return e
	}
	defer engine.Close()
	service, e := hosted.New(ctx, c, engine, budgeted)
	if e != nil {
		return e
	}
	defer service.Store.Close()
	server := &http.Server{Addr: *listen, Handler: service.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	results := make(chan error, 2)
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); results <- service.Run(ctx) }()
	go func() { results <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
	case e = <-results:
		cancel()
	}
	stopCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	_ = server.Shutdown(stopCtx)
	<-workerDone
	if errors.Is(e, context.Canceled) || errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
