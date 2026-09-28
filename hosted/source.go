package hosted

import (
	"archive/tar"
	"context"
	"database/sql"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func quote(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\\''") + "'" }
func (s *Service) syncSource(ctx context.Context) error {
	if s.Config.Repository != "git@github.com:example/lab-templates.git" {
		return fmt.Errorf("only the configured CDC lab repository is supported")
	}
	dir, e := os.MkdirTemp(s.Config.Root, "source-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(dir)
	env := append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -F /dev/null -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile="+quote(s.Config.KnownHosts)+" -i "+quote(s.Config.DeployKey))
	git := func(args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Env = env
		return cmd
	}
	bare := filepath.Join(dir, "repo.git")
	if _, e = git("clone", "--bare", "--depth=1", "--single-branch", "--branch=main", s.Config.Repository, bare).Output(); e != nil {
		return fmt.Errorf("read private template repository: %w", e)
	}
	b, e := git("--git-dir="+bare, "rev-parse", "HEAD").Output()
	if e != nil {
		return e
	}
	commit := strings.TrimSpace(string(b))
	metadata := func(revision string) (time.Time, string, error) {
		output, err := git("--git-dir="+bare, "show", "--no-patch", "--format=%cI%n%s", revision).Output()
		if err != nil {
			return time.Time{}, "", err
		}
		return parseCommitMetadata(string(output))
	}
	committed, title, e := metadata(commit)
	if e != nil {
		return e
	}

	cmd := git("--git-dir="+bare, "archive", commit, "templates")
	pipe, e := cmd.StdoutPipe()
	if e != nil {
		return e
	}
	if e = cmd.Start(); e != nil {
		return e
	}
	root := filepath.Join(dir, "snapshot")
	e = unpackSource(pipe, root)
	if e != nil {
		_ = cmd.Process.Kill()
	}
	wait := cmd.Wait()
	if e != nil {
		return e
	}
	if wait != nil {
		return wait
	}
	entries, e := os.ReadDir(filepath.Join(root, "templates"))
	if e != nil {
		return e
	}
	if len(entries) > 32 {
		return fmt.Errorf("too many templates")
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, "templates", entry.Name())
		src, e := lc.Load(path)
		if e != nil {
			return e
		}
		if _, e = lc.PlanLab(src, s.env, nil); e != nil {
			return e
		}
		id := digest(commit + ":" + entry.Name())[:24]
		handover, e := os.ReadFile(filepath.Join(path, "README.md"))
		if e != nil {
			return fmt.Errorf("template README is required: %w", e)
		}
		s.mu.Lock()
		var t Template
		if err := s.Store.Get(ctx, "template", id, &t); err != nil && !errors.Is(err, sql.ErrNoRows) {
			s.mu.Unlock()
			return err
		}
		t.ID = id
		t.Name = src.Lab.Name
		t.Commit = commit
		t.Handover = string(handover)
		t.CommittedAt = &committed
		t.CommitTitle = title
		if t.ImportedAt == nil {
			now := time.Now().UTC()
			t.ImportedAt = &now
		}
		if e = s.Store.Put(ctx, "source", id, src); e != nil {
			s.mu.Unlock()
			return e
		}
		if e = s.Store.Put(ctx, "template", id, t); e == nil {
			e = s.Store.Put(ctx, "template-head", t.Name, id)
		}
		s.mu.Unlock()
		if e != nil {
			return e
		}
	}
	// Backfill dates and subjects for revisions imported before timeline metadata.
	older, e := s.rawTemplates(ctx)
	if e != nil {
		return e
	}
	fetched := 0
	for _, old := range older {
		if old.CommittedAt != nil || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(old.Commit) || fetched >= 50 {
			continue
		}
		fetched++
		if _, e = git("--git-dir="+bare, "fetch", "--depth=1", "origin", old.Commit).Output(); e != nil {
			return fmt.Errorf("read revision metadata: %w", e)
		}
		at, subject, err := metadata(old.Commit)
		if err != nil {
			return err
		}
		s.mu.Lock()
		err = s.Store.Get(ctx, "template", old.ID, &old)
		if err == nil {
			old.CommittedAt = &at
			old.CommitTitle = subject
			err = s.Store.Put(ctx, "template", old.ID, old)
		}
		s.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}
func unpackSource(r io.Reader, root string) error {
	tr := tar.NewReader(io.LimitReader(r, 65<<20))
	var size int64
	count := 0
	for {
		h, e := tr.Next()
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
		count++
		if count > 2000 {
			return fmt.Errorf("too many source files")
		}
		// git archive emits a global PAX header carrying its commit comment.
		// This is metadata, never a filesystem entry.
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name := filepath.Clean(h.Name)
		if !strings.HasPrefix(name, "templates/") && name != "templates" {
			return fmt.Errorf("unexpected source path")
		}
		if filepath.IsAbs(h.Name) || strings.Contains(h.Name, "\\") {
			return fmt.Errorf("unsafe source path")
		}
		path := filepath.Join(root, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if e = os.MkdirAll(path, 0700); e != nil {
				return e
			}
		case tar.TypeReg:
			size += h.Size
			if h.Size < 0 || size > 64<<20 {
				return fmt.Errorf("source is too large")
			}
			if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
				return e
			}
			f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				return e
			}
			_, e = io.CopyN(f, tr, h.Size)
			ce := f.Close()
			if e != nil {
				return e
			}
			if ce != nil {
				return ce
			}
		default:
			return fmt.Errorf("source links and special files are not allowed")
		}
	}
}

func parseCommitMetadata(output string) (time.Time, string, error) {
	lines := strings.SplitN(strings.TrimSpace(output), "\n", 2)
	if len(lines) != 2 {
		return time.Time{}, "", fmt.Errorf("invalid commit metadata")
	}
	at, err := time.Parse(time.RFC3339, lines[0])
	if err != nil {
		return time.Time{}, "", err
	}
	title := []rune(strings.TrimSpace(lines[1]))
	if len(title) > 200 {
		title = title[:200]
	}
	return at, string(title), nil
}
