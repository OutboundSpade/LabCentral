// Package github resolves configured libraries using GitHub's read-only API.
// It never checks out a repository or executes hooks, workflows or build source.
package github

import (
	"bytes"
	"context"
	"crypto/sha1" // Git object identity; package identities use SHA-256.
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/OutboundSpade/LabCentral/pkg/labbundle"
	"go.yaml.in/yaml/v4"
)

var ErrRemoteUnavailable = errors.New("GitHub is temporarily unavailable")

const IndexVersion = "labcentral.library/v1"

type Configuration struct {
	Owner      string `json:"owner"`
	Repository string `json:"repository"`
	Reference  string `json:"reference"`
	Index      string `json:"index"`
}
type Index struct {
	APIVersion string  `yaml:"apiVersion" json:"apiVersion"`
	Templates  []Entry `yaml:"templates" json:"templates"`
}
type Entry struct {
	Key         string `yaml:"key" json:"key"`
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Root        string `yaml:"root" json:"root"`
}
type ResolvedEntry struct {
	Entry   Entry  `json:"entry"`
	Archive []byte `json:"archive"`
}
type Collection struct {
	Commit  string          `json:"commit"`
	Entries []ResolvedEntry `json:"entries"`
}

var repoName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
var entryKey = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var gitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

func ParseConfiguration(data []byte) (Configuration, error) {
	var c Configuration
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, fmt.Errorf("invalid GitHub configuration")
	}
	if d.Decode(new(any)) != io.EOF || !repoName.MatchString(c.Owner) || !repoName.MatchString(c.Repository) || len(c.Reference) == 0 || len(c.Reference) > 256 || strings.ContainsAny(c.Reference, "\r\n\x00") || !labbundle.ValidPath(c.Index) {
		return c, fmt.Errorf("invalid configured repository, reference or index")
	}
	return c, nil
}

type resolver struct {
	client     *http.Client
	base       string
	credential string
	prefix     string
}

func newResolver(c Configuration, credential []byte) *resolver {
	return &resolver{client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("GitHub redirects are disabled") }}, base: "https://api.github.com", prefix: "/repos/" + url.PathEscape(c.Owner) + "/" + url.PathEscape(c.Repository), credential: string(credential)}
}
func Resolve(ctx context.Context, c Configuration, credential []byte, pinned string, onPin func(string) error) (Collection, error) {
	return newResolver(c, credential).resolve(ctx, c, pinned, onPin)
}
func (r *resolver) get(ctx context.Context, p string, limit int64, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", r.base+r.prefix+p, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	req.Header.Set("User-Agent", "LabCentral-GitHub-source")
	if r.credential != "" {
		req.Header.Set("Authorization", "Bearer "+r.credential)
	}
	response, err := r.client.Do(req)
	if err != nil {
		return ErrRemoteUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("%w (HTTP %d)", ErrRemoteUnavailable, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return ErrRemoteUnavailable
	}
	if int64(len(body)) > limit {
		return fmt.Errorf("GitHub response exceeds bounds")
	}
	if json.Unmarshal(body, out) != nil {
		return fmt.Errorf("invalid GitHub response")
	}
	return nil
}

type treeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}

func (r *resolver) resolve(ctx context.Context, c Configuration, pinned string, onPin func(string) error) (Collection, error) {
	result := Collection{Entries: []ResolvedEntry{}}
	if pinned == "" {
		var commit struct {
			SHA string `json:"sha"`
		}
		if err := r.get(ctx, "/commits/"+url.PathEscape(c.Reference), 2<<20, &commit); err != nil {
			return result, err
		}
		if !gitSHA.MatchString(commit.SHA) {
			return result, fmt.Errorf("invalid resolved commit")
		}
		pinned = commit.SHA
		if err := onPin(pinned); err != nil {
			return result, err
		}
	}
	if !gitSHA.MatchString(pinned) {
		return result, fmt.Errorf("invalid pinned commit")
	}
	result.Commit = pinned
	var tree struct {
		Truncated bool        `json:"truncated"`
		Tree      []treeEntry `json:"tree"`
	}
	if err := r.get(ctx, "/git/trees/"+pinned+"?recursive=1", 8<<20, &tree); err != nil {
		return result, err
	}
	if tree.Truncated || len(tree.Tree) > 100000 {
		return result, fmt.Errorf("repository tree is incomplete or too large")
	}
	entries := map[string]treeEntry{}
	for _, entry := range tree.Tree {
		if _, ok := entries[entry.Path]; ok {
			return result, fmt.Errorf("duplicate tree path")
		}
		entries[entry.Path] = entry
	}
	cache := map[string][]byte{}
	totalFetched := 0
	read := func(name string) ([]byte, error) {
		entry, ok := entries[name]
		if !ok || !labbundle.ValidPath(name) || entry.Type != "blob" || (entry.Mode != "100644" && entry.Mode != "100755") || entry.Size < 0 || entry.Size > labbundle.MaxBytes || !gitSHA.MatchString(entry.SHA) {
			return nil, fmt.Errorf("source is missing, unsafe or too large")
		}
		if data, ok := cache[entry.SHA]; ok {
			return data, nil
		}
		var blob struct {
			SHA      string `json:"sha"`
			Encoding string `json:"encoding"`
			Content  string `json:"content"`
			Size     int64  `json:"size"`
		}
		if err := r.get(ctx, "/git/blobs/"+entry.SHA, 12<<20, &blob); err != nil {
			return nil, err
		}
		data, err := base64.StdEncoding.DecodeString(blob.Content)
		if err != nil || blob.Encoding != "base64" || blob.Size != int64(len(data)) || entry.Size != int64(len(data)) || blob.SHA != entry.SHA {
			return nil, fmt.Errorf("blob metadata mismatch")
		}
		hash := sha1.New()
		fmt.Fprintf(hash, "blob %d\x00", len(data))
		hash.Write(data)
		if hex.EncodeToString(hash.Sum(nil)) != entry.SHA {
			return nil, fmt.Errorf("blob identity mismatch")
		}
		totalFetched += len(data)
		if totalFetched > 64<<20 {
			return nil, fmt.Errorf("library source exceeds bound")
		}
		cache[entry.SHA] = data
		return data, nil
	}
	indexData, err := read(c.Index)
	if err != nil {
		return result, err
	}
	var index Index
	if err = decodeYAML(indexData, &index); err != nil || index.APIVersion != IndexVersion || index.Templates == nil || len(index.Templates) > 32 {
		return result, fmt.Errorf("invalid bounded library index")
	}
	seen := map[string]bool{}
	totalArchive := 0
	for _, entry := range index.Templates {
		if !entryKey.MatchString(entry.Key) || seen[entry.Key] || len(entry.Name) == 0 || len(entry.Name) > 63 || strings.TrimSpace(entry.Name) != entry.Name || len(entry.Description) > 4096 || !labbundle.ValidPath(entry.Root) {
			return result, fmt.Errorf("invalid library entry")
		}
		seen[entry.Key] = true
		root := path.Join(path.Dir(c.Index), entry.Root)
		manifestData, err := read(path.Join(root, "bundle.yaml"))
		if err != nil {
			return result, err
		}
		var manifest labbundle.Manifest
		if err = decodeYAML(manifestData, &manifest); err != nil || manifest.APIVersion != labbundle.PortableVersion || len(manifest.Files) == 0 || len(manifest.Files) >= labbundle.MaxFiles {
			return result, fmt.Errorf("invalid portable manifest")
		}
		files := map[string][]byte{}
		for _, file := range manifest.Files {
			if !labbundle.ValidPath(file.Path) || file.Path == "bundle.yaml" {
				return result, fmt.Errorf("invalid manifest file path")
			}
			if _, ok := files[file.Path]; ok {
				return result, fmt.Errorf("duplicate manifest source")
			}
			data, err := read(path.Join(root, file.Path))
			if err != nil {
				return result, err
			}
			files[file.Path] = data
		}
		archive, err := labbundle.Pack(manifest, files)
		if err != nil {
			return result, err
		}
		// Pack verifies dependency pins; independently verify the authored file table
		// so synchronization never silently refreshes stale source locks.
		packed, err := labbundle.Read(archive)
		if err != nil {
			return result, err
		}
		byPath := map[string]labbundle.File{}
		for _, file := range packed.Manifest.Files {
			byPath[file.Path] = file
		}
		for _, file := range manifest.Files {
			if file != byPath[file.Path] {
				return result, fmt.Errorf("source file index is stale; run lab-template lock")
			}
		}
		totalArchive += len(archive)
		if totalArchive > 64<<20 {
			return result, fmt.Errorf("packaged library exceeds bound")
		}
		result.Entries = append(result.Entries, ResolvedEntry{Entry: entry, Archive: archive})
	}
	return result, nil
}
func decodeYAML(data []byte, out any) error {
	if len(data) > 1<<20 {
		return fmt.Errorf("metadata too large")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("trailing YAML")
	}
	return nil
}
