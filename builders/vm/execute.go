package vm

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	spb "github.com/moby/buildkit/sourcepolicy/pb"
)

const guestProxy = "http://10.0.2.100:3128"

func guestEnvironment() []string {
	return []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/root", "LANG=C.UTF-8", "HTTP_PROXY=" + guestProxy, "HTTPS_PROXY=" + guestProxy, "http_proxy=" + guestProxy, "https_proxy=" + guestProxy, "NO_PROXY=localhost,127.0.0.1", "PACKER_PLUGIN_PATH=/opt/labcentral/packer-plugins", "LIBGUESTFS_BACKEND=direct"}
}

type boundedLog struct {
	f         *os.File
	remaining int64
}

func (w *boundedLog) Write(data []byte) (int, error) {
	n := len(data)
	if w.remaining > 0 {
		part := data[:min(int64(len(data)), w.remaining)]
		written, err := w.f.Write(part)
		w.remaining -= int64(written)
		if err != nil {
			return written, err
		}
	}
	return n, nil
}
func guestCommand(ctx context.Context, root, name string, args ...string) error {
	log, err := os.OpenFile(filepath.Join(root, "build.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	defer log.Sync()
	stat, err := log.Stat()
	if err != nil {
		return err
	}
	writer := &boundedLog{f: log, remaining: (8 << 20) - stat.Size()}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = root
	cmd.Env = guestEnvironment()
	cmd.Stdout = writer
	cmd.Stderr = writer
	cmd.WaitDelay = 5 * time.Second
	if err = cmd.Run(); err != nil {
		_, _ = fmt.Fprintf(log, "command %s failed: %v\n", name, err)
		return fmt.Errorf("guest build command failed: %s", name)
	}
	return nil
}
func ExecuteGuestBuild(ctx context.Context, root string, recipe *wire.BuildRecipe, limits *wire.BuildLimits) (string, string, error) {
	if err := ValidateRecipe(recipe, limits); err != nil {
		return "", "", err
	}
	data, err := os.ReadFile(filepath.Join(root, "inputs", recipe.Inputs[0].Digest))
	if err != nil {
		return "", "", err
	}
	source, err := ParseContext(data)
	if err != nil {
		return "", "", err
	}
	contextDir := filepath.Join(root, "context")
	if err = os.Mkdir(contextDir, 0700); err != nil {
		return "", "", err
	}
	for _, file := range source.Files {
		target := filepath.Join(contextDir, file.Path)
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return "", "", err
		}
		if err = os.WriteFile(target, file.Data, 0600); err != nil {
			return "", "", err
		}
	}
	switch recipe.Configuration.Schema.Name {
	case DockerfileSchema:
		return executeDockerfile(ctx, root, recipe, source, limits)
	case AnsibleSchema:
		return executeAnsible(ctx, root, recipe, source, limits)
	default:
		return "", "", fmt.Errorf("unsupported recipe")
	}
}
func buildSourcePolicy(digests []string) *spb.Policy {
	p := &spb.Policy{Version: 1, Rules: []*spb.Rule{{Action: spb.PolicyAction_DENY, Selector: &spb.Selector{Identifier: "*"}}, {Action: spb.PolicyAction_ALLOW, Selector: &spb.Selector{Identifier: "local://*"}}, {Action: spb.PolicyAction_ALLOW, Selector: &spb.Selector{Identifier: "oci-layout://*"}}}}
	for _, digest := range digests {
		p.Rules = append(p.Rules, &spb.Rule{Action: spb.PolicyAction_ALLOW, Selector: &spb.Selector{Identifier: "docker-image://*@sha256:" + strings.TrimPrefix(digest, "sha256:")}})
	}
	return p
}
func executeDockerfile(ctx context.Context, root string, recipe *wire.BuildRecipe, source SourceContext, limits *wire.BuildLimits) (string, string, error) {
	var settings DockerfileSettings
	_ = strictJSON(recipe.Configuration.CanonicalJson, &settings)
	data, err := os.ReadFile(filepath.Join(root, "context", settings.Dockerfile))
	if err != nil {
		return "", "", err
	}
	names := []string{}
	for _, dep := range recipe.Dependencies {
		names = append(names, dep.RecipeId)
	}
	if err = ValidateDockerfile(data, recipe.BaseImageDigests, names); err != nil {
		return "", "", err
	}
	policy, err := json.Marshal(buildSourcePolicy(recipe.BaseImageDigests))
	if err != nil {
		return "", "", err
	}
	if err = os.WriteFile(filepath.Join(root, "policy.json"), policy, 0600); err != nil {
		return "", "", err
	}
	daemonCtx, stop := context.WithCancel(ctx)
	defer stop()
	daemon := exec.CommandContext(daemonCtx, "/usr/local/bin/buildkitd", "--addr", "unix:///run/labcentral-buildkit.sock", "--root", filepath.Join(root, "buildkit"), "--oci-worker=true", "--containerd-worker=false", "--oci-worker-net=host")
	daemon.Env = guestEnvironment()
	daemonLog, err := os.OpenFile(filepath.Join(root, "buildkit.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return "", "", err
	}
	defer daemonLog.Close()
	defer daemonLog.Sync()
	daemonWriter := &boundedLog{f: daemonLog, remaining: 8 << 20}
	daemon.Stdout = daemonWriter
	daemon.Stderr = daemonWriter
	if err = daemon.Start(); err != nil {
		return "", "", err
	}
	defer func() { stop(); _ = daemon.Wait() }()
	ready := false
	for count := 0; count < 100; count++ {
		if _, err = os.Stat("/run/labcentral-buildkit.sock"); err == nil {
			ready = true
			break
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !ready {
		return "", "", fmt.Errorf("BuildKit did not start")
	}
	output := filepath.Join(root, "result.oci.tar")
	args := []string{"--addr", "unix:///run/labcentral-buildkit.sock", "build", "--frontend", "dockerfile.v0", "--local", "context=" + filepath.Join(root, "context"), "--local", "dockerfile=" + filepath.Join(root, "context"), "--opt", "filename=" + settings.Dockerfile, "--opt", "platform=linux/amd64", "--source-policy-file", filepath.Join(root, "policy.json"), "--output", "type=oci,oci-mediatypes=true,dest=" + output, "--progress", "plain"}
	if settings.Target != "" {
		args = append(args, "--opt", "target="+settings.Target)
	}
	for _, input := range source.Inputs {
		if strings.HasPrefix(input.Name, "BUILDKIT_") || strings.HasPrefix(input.Name, "BUILDX_") {
			return "", "", fmt.Errorf("reserved build argument")
		}
		args = append(args, "--opt", "build-arg:"+input.Name+"="+input.Value)
	}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		args = append(args, "--opt", "build-arg:"+key+"="+guestProxy)
	}
	for _, dep := range recipe.Dependencies {
		dir := filepath.Join(root, "oci-"+dep.RecipeId)
		manifest, err := unpackOCI(filepath.Join(root, "inputs", dep.Artifact.Digest), dir, limits.MaximumInputBytes)
		if err != nil {
			return "", "", err
		}
		args = append(args, "--oci-layout", dep.RecipeId+"="+dir, "--opt", "context:"+dep.RecipeId+"=oci-layout://"+dep.RecipeId+"@"+manifest)
	}
	if err = guestCommand(ctx, root, "/usr/local/bin/buildctl", args...); err != nil {
		return "", "", err
	}
	return output, OCIMedia, nil
}
func unpackOCI(input, dir string, maximum uint64) (string, error) {
	if err := os.Mkdir(dir, 0700); err != nil {
		return "", err
	}
	f, err := os.Open(input)
	if err != nil {
		return "", err
	}
	defer f.Close()
	reader := tar.NewReader(f)
	var total uint64
	for entries := 0; ; entries++ {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil || entries >= 100000 {
			return "", fmt.Errorf("invalid OCI archive")
		}
		name := strings.TrimSuffix(header.Name, "/")
		if !sourcePath(name) || header.Size < 0 || uint64(header.Size) > maximum-total {
			return "", fmt.Errorf("unsafe OCI archive")
		}
		total += uint64(header.Size)
		target := filepath.Join(dir, name)
		if header.Typeflag == tar.TypeDir {
			if err = os.MkdirAll(target, 0700); err != nil {
				return "", err
			}
			continue
		}
		if header.Typeflag != tar.TypeReg {
			return "", fmt.Errorf("nonregular OCI entry")
		}
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return "", err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return "", err
		}
		_, err = io.CopyN(out, reader, header.Size)
		closeErr := out.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil || len(data) > 1<<20 {
		return "", fmt.Errorf("invalid OCI index")
	}
	var index struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if json.Unmarshal(data, &index) != nil || len(index.Manifests) != 1 || !strings.HasPrefix(index.Manifests[0].Digest, "sha256:") || !digestValid(strings.TrimPrefix(index.Manifests[0].Digest, "sha256:")) {
		return "", fmt.Errorf("one digest-pinned OCI manifest required")
	}
	return index.Manifests[0].Digest, nil
}
func downloadBase(ctx context.Context, location, digest, destination string, maximum uint64) error {
	proxy, _ := url.Parse(guestProxy)
	transport := &http.Transport{Proxy: http.ProxyURL(proxy)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(next *http.Request, previous []*http.Request) error {
		if len(previous) > 5 || next.URL.Scheme != "https" || next.URL.User != nil || (next.URL.Port() != "" && next.URL.Port() != "443") || next.URL.Fragment != "" {
			return fmt.Errorf("base redirect violates HTTPS policy")
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, "GET", location, nil)
	if err != nil {
		return err
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("base download failed")
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, hash), io.LimitReader(res.Body, int64(maximum)+1))
	if err != nil {
		return err
	}
	if n <= 0 || uint64(n) > maximum || hex.EncodeToString(hash.Sum(nil)) != digest {
		return fmt.Errorf("base verification failed")
	}
	return out.Sync()
}
