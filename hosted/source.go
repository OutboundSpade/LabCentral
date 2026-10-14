package hosted

import (
	"archive/tar"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/instructor"
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
	if !regexp.MustCompile(`^git@github\.com:[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\.git$`).MatchString(s.Config.Repository) {
		return fmt.Errorf("repository must be an SSH GitHub URL")
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
	if e = s.importSnapshot(ctx, filepath.Join(root, "templates"), commit, committed, title); e != nil {
		return e
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
		_, err = s.Store.DB.ExecContext(ctx, `UPDATE objects SET body=json_set(body,'$.committedAt',json(?),'$.commitTitle',?) WHERE kind='template' AND id=?`, jsonValue(at), subject, old.ID)
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

// Import failures describe the most recent refresh. Infrastructure failures still
// fail the refresh; an invalid directory does not suppress valid neighbours.
type TemplateImportFailure struct {
	Directory string `json:"directory"`
	Error     string `json:"error"`
}
type TemplateImportReport struct {
	Commit   string                  `json:"commit"`
	Failures []TemplateImportFailure `json:"failures"`
}

func (s *Service) importSnapshot(ctx context.Context, root, commit string, committed time.Time, title string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	if len(entries) > 32 {
		return fmt.Errorf("too many templates")
	}
	report := TemplateImportReport{Commit: commit, Failures: []TemplateImportFailure{}}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		dir := filepath.Join(root, entry.Name())
		src, b, err := instructor.LoadTemplate(dir, true)
		if err == nil {
			_, err = lc.PlanLab(src, s.env, nil)
		}
		if err != nil {
			report.Failures = append(report.Failures, TemplateImportFailure{entry.Name(), err.Error()})
			continue
		}
		handover, err := os.ReadFile(filepath.Join(dir, "README.md"))
		if err != nil {
			return err
		}
		// Private material stays out of the disposable hosted snapshot as well.
		if b != nil {
			if err = os.Remove(filepath.Join(dir, "instructor.yaml")); err != nil {
				return err
			}
			if err = os.RemoveAll(filepath.Join(dir, "instructor")); err != nil {
				return err
			}
		}
		id := digest(commit + ":" + entry.Name())[:24]
		err = s.Store.importRevision(ctx, id, src, b, string(handover), commit, committed, title)
		if err != nil {
			return err
		}
	}
	return s.Store.Put(ctx, "template-import-report", "current", report)
}

// All four records commit together. Re-import preserves build/cleanup state and
// the original import date, while safely refreshing the matching head.
func (s *Store) importRevision(ctx context.Context, id string, src lc.Source, b *InstructorBundle, handover, commit string, committed time.Time, title string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var t Template
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT body FROM objects WHERE kind='template' AND id=?`, id).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, &t); err != nil {
			return err
		}
	}
	t.ID = id
	t.Name = src.Lab.Name
	t.Commit = commit
	t.Handover = handover
	t.CommittedAt = &committed
	t.CommitTitle = title
	if t.ImportedAt == nil {
		now := time.Now().UTC()
		t.ImportedAt = &now
	}
	if b != nil {
		v := instructorView(*b)
		t.Instructor = &v
	}
	put := func(kind, id string, value any) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO objects(kind,id,body) VALUES(?,?,?) ON CONFLICT(kind,id) DO UPDATE SET body=excluded.body`, kind, id, raw)
		return err
	}
	if b != nil {
		if err = put("template-instructor", id, b); err != nil {
			return err
		}
	}
	if err = put("source", id, src); err != nil {
		return err
	}
	if err = put("template", id, t); err != nil {
		return err
	}
	if err = put("template-head", t.Name, id); err != nil {
		return err
	}
	return tx.Commit()
}
