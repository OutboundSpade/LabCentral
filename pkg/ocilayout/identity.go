// Package ocilayout verifies source-independent OCI artifact identities without
// extracting or executing image contents. Runtime extraction belongs in a VM.
package ocilayout

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

const MediaType = "application/vnd.oci.image.layout.v1.tar"

type Identity struct {
	ManifestDigest string `json:"manifestDigest"`
	ConfigDigest   string `json:"configDigest"`
	Architecture   string `json:"architecture"`
	OS             string `json:"os"`
}

func SHA256(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value[7:])
	return err == nil
}
func (i Identity) Validate() error {
	if !SHA256(i.ManifestDigest) || !SHA256(i.ConfigDigest) || i.Architecture != "amd64" || i.OS != "linux" {
		return fmt.Errorf("Linux amd64 OCI identity required")
	}
	return nil
}

type blob struct {
	size     int64
	metadata []byte
}

// Inspect checks the complete archive, bounded metadata, every content-addressed
// blob, and one runnable Linux amd64 image. No archive path reaches the filesystem.
func Inspect(reader io.Reader, maximum uint64) (Identity, error) {
	fail := func() (Identity, error) { return Identity{}, fmt.Errorf("invalid or unsupported OCI layout") }
	if maximum == 0 || maximum > 32<<30 {
		return fail()
	}
	bounded := &io.LimitedReader{R: reader, N: int64(maximum) + 1}
	archive := tar.NewReader(bounded)
	seen := map[string]bool{}
	blobs := map[string]blob{}
	var indexData, layoutData []byte
	var metadataBytes int64
	for entries := 0; ; entries++ {
		h, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil || entries >= 4096 {
			return fail()
		}
		name := strings.TrimSuffix(h.Name, "/")
		if name == "" || path.Clean(name) != name || strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\\x00") || seen[name] || h.Size < 0 || uint64(h.Size) > maximum {
			return fail()
		}
		seen[name] = true
		if h.Typeflag == tar.TypeDir {
			if h.Size != 0 || (name != "blobs" && name != "blobs/sha256") {
				return fail()
			}
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return fail()
		}
		if name == "index.json" || name == "oci-layout" {
			if h.Size > 1<<20 {
				return fail()
			}
			data, err := io.ReadAll(archive)
			if err != nil {
				return fail()
			}
			if name == "index.json" {
				indexData = data
			} else {
				layoutData = data
			}
			continue
		}
		digest := "sha256:" + strings.TrimPrefix(name, "blobs/sha256/")
		if !strings.HasPrefix(name, "blobs/sha256/") || !SHA256(digest) {
			return fail()
		}
		hash := sha256.New()
		var metadata bytes.Buffer
		var output io.Writer = hash
		if h.Size <= 1<<20 {
			metadataBytes += h.Size
			if metadataBytes > 16<<20 {
				return fail()
			}
			output = io.MultiWriter(hash, &metadata)
		}
		n, err := io.Copy(output, archive)
		if err != nil || n != h.Size || hex.EncodeToString(hash.Sum(nil)) != digest[7:] {
			return fail()
		}
		blobs[digest] = blob{size: n, metadata: metadata.Bytes()}
	}
	// Tar end markers do not excuse a truncated bound or hidden trailing content.
	tail, err := io.ReadAll(io.LimitReader(bounded, 1025))
	if err != nil || len(tail) > 1024 || bounded.N <= 0 {
		return fail()
	}
	for _, b := range tail {
		if b != 0 {
			return fail()
		}
	}
	var layout v1.ImageLayout
	var index v1.Index
	if json.Unmarshal(layoutData, &layout) != nil || layout.Version != "1.0.0" || json.Unmarshal(indexData, &index) != nil || index.SchemaVersion != 2 || len(index.Manifests) != 1 {
		return fail()
	}
	descriptor := index.Manifests[0]
	verify := func(d v1.Descriptor) (blob, bool) {
		b, ok := blobs[d.Digest.String()]
		return b, ok && SHA256(d.Digest.String()) && d.Size == b.size && len(d.URLs) == 0 && len(d.Data) == 0
	}
	// Allow nested indexes from exporters, still requiring one runnable image.
	for depth := 0; descriptor.MediaType == v1.MediaTypeImageIndex; depth++ {
		b, ok := verify(descriptor)
		if !ok || depth >= 3 || len(b.metadata) == 0 {
			return fail()
		}
		if json.Unmarshal(b.metadata, &index) != nil || index.SchemaVersion != 2 || len(index.Manifests) != 1 {
			return fail()
		}
		descriptor = index.Manifests[0]
	}
	b, ok := verify(descriptor)
	if !ok || descriptor.MediaType != v1.MediaTypeImageManifest || len(b.metadata) == 0 {
		return fail()
	}
	if descriptor.Platform != nil && (descriptor.Platform.OS != "linux" || descriptor.Platform.Architecture != "amd64") {
		return fail()
	}
	var manifest v1.Manifest
	if json.Unmarshal(b.metadata, &manifest) != nil || manifest.SchemaVersion != 2 || manifest.ArtifactType != "" || manifest.Subject != nil || len(manifest.Layers) > 256 || manifest.Config.MediaType != v1.MediaTypeImageConfig {
		return fail()
	}
	config, ok := verify(manifest.Config)
	if !ok || len(config.metadata) == 0 {
		return fail()
	}
	var image v1.Image
	if json.Unmarshal(config.metadata, &image) != nil || image.RootFS.Type != "layers" || len(image.RootFS.DiffIDs) != len(manifest.Layers) {
		return fail()
	}
	for n, layer := range manifest.Layers {
		if _, ok := verify(layer); !ok {
			return fail()
		}
		if layer.MediaType != v1.MediaTypeImageLayer && layer.MediaType != v1.MediaTypeImageLayerGzip && layer.MediaType != v1.MediaTypeImageLayerZstd {
			return fail()
		}
		if !SHA256(image.RootFS.DiffIDs[n].String()) {
			return fail()
		}
	}
	identity := Identity{ManifestDigest: descriptor.Digest.String(), ConfigDigest: manifest.Config.Digest.String(), Architecture: image.Architecture, OS: image.OS}
	return identity, identity.Validate()
}
