//go:build windows

package main

import (
	"context"
	"fmt"
	"io"
)

func runArtifactGC(context.Context, []string, io.Writer) error {
	return fmt.Errorf("filesystem artifact maintenance is unavailable on Windows")
}

func runNativeArtifactGC(context.Context, []string, io.Writer) error {
	return fmt.Errorf("native artifact maintenance is unavailable on Windows")
}
