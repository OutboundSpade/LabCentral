// This independent application imports only public LabCentral packages.
package main

import (
	"context"
	"flag"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/access"
	"github.com/OutboundSpade/LabCentral/v2/plugin"
	"os"
	"os/signal"
	"path/filepath"
	"time"
)

func run(ctx context.Context) error {
	sourcePath := flag.String("lab", "../single", "lab directory")
	envPath := flag.String("environment", "", "operator environment file")
	root := flag.String("state", "./state", "private state directory")
	binary := flag.String("provider", "../../bin/labcentral-provider", "provider executable")
	flag.Parse()
	source, err := lc.Load(*sourcePath)
	if err != nil {
		return err
	}
	env, err := lc.LoadEnvironment(*envPath)
	if err != nil {
		return err
	}
	plan, err := lc.PlanLab(source, env, nil)
	if err != nil {
		return err
	}
	process, err := plugin.Start(ctx, *binary, filepath.Join(*root, "providers"))
	if err != nil {
		return err
	}
	defer process.Close()
	engine, err := lc.Open(*root, process, func(e lc.Event) { fmt.Fprintf(os.Stderr, "%s %s\n", e.Stage, e.Resource) })
	if err != nil {
		return err
	}
	defer engine.Close()
	images, err := engine.Build(ctx, plan)
	if err != nil {
		return err
	}
	instance, err := engine.Deploy(ctx, plan)
	if err != nil && instance.ID != "" {
		instance, err = engine.Recover(ctx, instance.ID)
	}
	if err != nil {
		return err
	}
	fmt.Printf("Instance: %s\n", instance.ID)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := engine.Destroy(cleanup, instance.ID); err != nil {
			fmt.Fprintln(os.Stderr, "retained instance:", instance.ID, err)
		}
	}()
	inspected, err := engine.Inspect(ctx, instance.ID)
	if err != nil {
		return err
	}
	machine := inspected.Plan.Machines[0]
	if lc.Contains(machine.Spec.Access, "desktop") {
		session, err := engine.OpenDesktop(ctx, instance.ID, machine.Name)
		if err != nil {
			return err
		}
		fmt.Printf("Open %s\nGuest: %s / %s\nPress Enter to close.\n", session.URL, session.Username, session.Password)
		_, _ = fmt.Scanln()
		if err = engine.CloseAccess(ctx, session.ID); err != nil {
			return err
		}
	} else if lc.Contains(machine.Spec.Access, "ssh") {
		code, err := access.Shell(ctx, engine, instance.ID, machine.Name, os.Stdin, os.Stdout, os.Stderr)
		if err != nil {
			return err
		}
		fmt.Println("Remote exit:", code)
	}
	if err = engine.Destroy(ctx, instance.ID); err != nil {
		return err
	}
	for _, image := range images {
		if err = engine.RemoveImage(ctx, image.Key); err != nil {
			return err
		}
	}
	return nil
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
