package localsecret

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
)

const defaultMaximumSecretBytes = 64 << 10

type FileResolver struct {
	allowed        map[string]struct{}
	maxBytes       int64
	allowGroupRead bool
}

type Options struct {
	// AllowGroupRead permits immutable orchestrator projections whose group has
	// read, but never write/execute, permission. The projected volume must be
	// mounted only into the control plane container.
	AllowGroupRead bool
}

func NewFileResolver(allowedFiles []string, maximumBytes int64) (*FileResolver, error) {
	return NewFileResolverWithOptions(allowedFiles, maximumBytes, Options{})
}

func NewFileResolverWithOptions(allowedFiles []string, maximumBytes int64, options Options) (*FileResolver, error) {
	if len(allowedFiles) == 0 || len(allowedFiles) > 64 {
		return nil, fmt.Errorf("local secret resolver requires between 1 and 64 allowed files")
	}
	if maximumBytes == 0 {
		maximumBytes = defaultMaximumSecretBytes
	}
	if maximumBytes < 1 || maximumBytes > 1<<20 {
		return nil, fmt.Errorf("local secret size limit must be between 1 byte and 1 MiB")
	}
	resolver := &FileResolver{allowed: make(map[string]struct{}, len(allowedFiles)), maxBytes: maximumBytes, allowGroupRead: options.AllowGroupRead}
	for _, configured := range allowedFiles {
		if !filepath.IsAbs(configured) || filepath.Clean(configured) != configured {
			return nil, fmt.Errorf("allowed local secret paths must be absolute and clean")
		}
		if _, duplicate := resolver.allowed[configured]; duplicate {
			return nil, fmt.Errorf("allowed local secret path is duplicated")
		}
		resolver.allowed[configured] = struct{}{}
	}
	return resolver, nil
}

func (resolver *FileResolver) Resolve(ctx context.Context, request application.AuthorizedSecretRef, scope application.InvocationScope) (*application.EphemeralSecret, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if resolver == nil || request.SubjectID == "" || request.Reference.Source != "file" || request.Reference.Key != "value" {
		return nil, fmt.Errorf("local secret request is invalid")
	}
	path := request.Reference.Name
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("local secret reference is invalid")
	}
	if _, allowed := resolver.allowed[path]; !allowed {
		return nil, fmt.Errorf("local secret reference is not allowlisted")
	}
	if scope.Validate(time.Now()) != nil {
		return nil, fmt.Errorf("local secret invocation scope is invalid")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || !resolver.allowedMode(before.Mode().Perm()) {
		return nil, fmt.Errorf("local secret file is unavailable or insufficiently protected")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open local secret file")
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Mode().Perm() != before.Mode().Perm() || !resolver.allowedMode(after.Mode().Perm()) {
		return nil, fmt.Errorf("local secret file identity changed")
	}
	contents, err := io.ReadAll(io.LimitReader(file, resolver.maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read local secret file")
	}
	if len(contents) == 0 || int64(len(contents)) > resolver.maxBytes {
		zero(contents)
		return nil, fmt.Errorf("local secret file is empty or exceeds its size limit")
	}
	return application.NewEphemeralSecret(contents), nil
}

func (resolver *FileResolver) allowedMode(mode os.FileMode) bool {
	if mode&0o400 == 0 {
		return false
	}
	if resolver.allowGroupRead {
		return mode&0o027 == 0
	}
	return mode&0o077 == 0
}

func zero(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

var _ application.SecretResolver = (*FileResolver)(nil)
