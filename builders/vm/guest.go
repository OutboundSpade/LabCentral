package vm

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"google.golang.org/protobuf/proto"
)

// GuestAgent is installed in the pinned toolchain image and talks only through
// virtio-serial. It has no worker, provider, or control-plane credentials.
// Everything this process reports remains untrusted to the host.
type GuestAgent struct {
	mu      sync.Mutex
	root    string
	recipe  *wire.BuildRecipe
	limits  *wire.BuildLimits
	state   string
	result  GuestResponse
	execute func(context.Context, string, *wire.BuildRecipe, *wire.BuildLimits) (string, string, error)
}

func NewGuestAgent(root string) (*GuestAgent, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("absolute guest state directory required")
	}
	if err := os.MkdirAll(filepath.Join(root, "inputs"), 0700); err != nil {
		return nil, err
	}
	return &GuestAgent{root: root, state: "empty", execute: ExecuteGuestBuild}, nil
}
func (g *GuestAgent) Serve(channel io.ReadWriter) error {
	scanner := bufio.NewScanner(channel)
	scanner.Buffer(make([]byte, 4096), channelFrameLimit)
	writer := json.NewEncoder(channel)
	for scanner.Scan() {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var request GuestRequest
		if err := strictJSON(scanner.Bytes(), &request); err != nil {
			continue
		}
		response := g.handle(request)
		response.ID = request.ID
		response.Version = 1
		if err := writer.Encode(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}
func (g *GuestAgent) handle(in GuestRequest) GuestResponse {
	g.mu.Lock()
	defer g.mu.Unlock()
	fail := func() GuestResponse { return GuestResponse{State: g.state, Error: "invalid guest build request"} }
	if in.Version != 1 || !safeName.MatchString(in.ID) {
		return fail()
	}
	switch in.Method {
	case "status":
		return g.status()
	case "configure":
		if ValidateRecipe(in.Recipe, in.Limits) != nil {
			return fail()
		}
		if g.recipe != nil {
			if !proto.Equal(g.recipe, in.Recipe) || !proto.Equal(g.limits, in.Limits) {
				return fail()
			}
			return g.status()
		}
		g.recipe = proto.Clone(in.Recipe).(*wire.BuildRecipe)
		g.limits = proto.Clone(in.Limits).(*wire.BuildLimits)
		g.state = "staging"
		return g.status()
	case "upload":
		if g.state != "staging" || len(in.Chunk) == 0 || len(in.Chunk) > channelChunkSize {
			return fail()
		}
		var expected *wire.ArtifactHandle
		for _, a := range g.recipe.Inputs {
			if a.Digest == in.Digest {
				expected = a
			}
		}
		for _, d := range g.recipe.Dependencies {
			if d.Artifact.Digest == in.Digest {
				expected = d.Artifact
			}
		}
		if expected == nil || !digestValid(in.Digest) || expected.Size > g.limits.MaximumInputBytes || in.Offset > expected.Size || uint64(len(in.Chunk)) > expected.Size-in.Offset {
			return fail()
		}
		f, err := os.OpenFile(filepath.Join(g.root, "inputs", in.Digest), os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return fail()
		}
		defer f.Close()
		if _, err = f.WriteAt(in.Chunk, int64(in.Offset)); err != nil {
			return fail()
		}
		return g.status()
	case "start":
		if g.state != "staging" {
			if g.state == "empty" {
				return fail()
			}
			return g.status()
		}
		artifacts := append([]*wire.ArtifactHandle{}, g.recipe.Inputs...)
		for _, d := range g.recipe.Dependencies {
			artifacts = append(artifacts, d.Artifact)
		}
		var total uint64
		for _, a := range artifacts {
			total += a.Size
			if total > g.limits.MaximumInputBytes || verifyArtifactFile(filepath.Join(g.root, "inputs", a.Digest), a.Digest, a.Size) != nil {
				return fail()
			}
		}
		g.state = "running"
		go g.run()
		return g.status()
	case "read":
		if g.state != "succeeded" || in.Offset >= g.result.Size {
			return fail()
		}
		f, err := os.Open(filepath.Join(g.root, "output"))
		if err != nil {
			return fail()
		}
		defer f.Close()
		size := min(uint64(channelChunkSize), g.result.Size-in.Offset)
		chunk := make([]byte, size)
		if _, err = f.ReadAt(chunk, int64(in.Offset)); err != nil {
			return fail()
		}
		return GuestResponse{State: g.state, Chunk: chunk}
	default:
		return fail()
	}
}
func (g *GuestAgent) status() GuestResponse { out := g.result; out.State = g.state; return out }
func (g *GuestAgent) run() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(g.limits.TimeoutSeconds)*time.Second)
	defer cancel()
	output, media, err := g.execute(ctx, g.root, g.recipe, g.limits)
	var result GuestResponse
	if err == nil {
		info, e := os.Lstat(output)
		if e != nil || !info.Mode().IsRegular() || info.Size() <= 0 || uint64(info.Size()) > g.limits.MaximumOutputBytes {
			err = fmt.Errorf("invalid guest output")
		} else {
			f, e := os.Open(output)
			if e != nil {
				err = e
			} else {
				hash := sha256.New()
				_, err = io.Copy(hash, io.LimitReader(f, int64(g.limits.MaximumOutputBytes)+1))
				f.Close()
				if err == nil {
					result = GuestResponse{Digest: hex.EncodeToString(hash.Sum(nil)), Size: uint64(info.Size()), MediaType: media}
					if output != filepath.Join(g.root, "output") {
						err = os.Rename(output, filepath.Join(g.root, "output"))
					}
				}
			}
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil {
		message := []byte(err.Error())
		if len(message) > 4096 {
			message = message[:4096]
		}
		if f, writeErr := os.OpenFile(filepath.Join(g.root, "failure.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600); writeErr == nil {
			_, _ = f.Write(message)
			_ = f.Sync()
			_ = f.Close()
		}
		if d, openErr := os.Open(g.root); openErr == nil {
			_ = d.Sync()
			_ = d.Close()
		}
		g.state = "failed"
		return
	}
	g.result = result
	g.state = "succeeded"
}
func verifyArtifactFile(path, digest string, size uint64) error {
	if !digestValid(digest) || size == 0 || size > 128<<30 {
		return fmt.Errorf("invalid artifact")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || uint64(info.Size()) != size {
		return fmt.Errorf("artifact size mismatch")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, int64(size)+1))
	if err != nil {
		return err
	}
	if uint64(n) != size || hex.EncodeToString(hash.Sum(nil)) != digest {
		return fmt.Errorf("artifact digest mismatch")
	}
	return nil
}
