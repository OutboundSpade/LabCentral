// Package sourceidentity hashes the content of a Go executable's local build inputs.
package sourceidentity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

type buildPackage struct {
	Dir                                                                                                                 string
	Standard                                                                                                            bool
	Module                                                                                                              *struct{ Main bool }
	GoFiles, CgoFiles, CFiles, CXXFiles, MFiles, HFiles, FFiles, SFiles, SwigFiles, SwigCXXFiles, SysoFiles, EmbedFiles []string
}

func Digest(ctx context.Context, root, target string) (string, error) {
	return DigestForBuild(ctx, root, target, "go", "", "")
}
func DigestForBuild(ctx context.Context, root, target, goBinary, goos, goarch string) (string, error) {
	cmd := exec.CommandContext(ctx, goBinary, "list", "-buildvcs=false", "-deps", "-json", target)
	cmd.Env = os.Environ()
	if goos != "" {
		cmd.Env = append(cmd.Env, "GOOS="+goos)
	}
	if goarch != "" {
		cmd.Env = append(cmd.Env, "GOARCH="+goarch)
	}
	cmd.Dir = root
	b, err := cmd.Output()
	if err != nil {
		if failure, ok := err.(*exec.ExitError); ok {
			detail := failure.Stderr
			if len(detail) > 4096 {
				detail = detail[:4096]
			}
			return "", fmt.Errorf("enumerate provider build inputs: %w: %s", err, detail)
		}
		return "", fmt.Errorf("enumerate provider build inputs: %w", err)
	}
	files := map[string]bool{"go.mod": true, "go.sum": true}
	decoder := json.NewDecoder(bytes.NewReader(b))
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	for {
		var p buildPackage
		err := decoder.Decode(&p)
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if p.Standard || p.Module == nil || !p.Module.Main {
			continue
		}
		for _, list := range [][]string{p.GoFiles, p.CgoFiles, p.CFiles, p.CXXFiles, p.MFiles, p.HFiles, p.FFiles, p.SFiles, p.SwigFiles, p.SwigCXXFiles, p.SysoFiles, p.EmbedFiles} {
			for _, name := range list {
				rel, err := filepath.Rel(abs, filepath.Join(p.Dir, name))
				if err != nil || !filepath.IsLocal(rel) {
					return "", fmt.Errorf("build input escapes source tree")
				}
				files[filepath.ToSlash(rel)] = true
			}
		}
	}
	return DigestFiles(abs, files)
}

// DigestFiles frames paths and contents to avoid ambiguous concatenations.
func DigestFiles(root string, files map[string]bool) (string, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		if !filepath.IsLocal(name) {
			return "", fmt.Errorf("invalid build input")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	fmt.Fprint(h, "labcentral.provider-source/v1\x00")
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%d:%s%d:", len(name), name, len(b))
		h.Write(b)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}
