package instructor

import (
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"os"
	"path/filepath"
	"strings"
)

func invalid(field, message string) error {
	return &lc.Error{Code: lc.Invalid, Field: field, Message: message}
}
func Parse(raw []byte) (*Bundle, error) {
	var b Bundle
	if err := lc.DecodeStrictYAML(raw, &b, lc.YAMLLimits{MaxBytes: 128 << 10}); err != nil {
		return nil, lc.YAMLLineError(raw, err)
	}
	b.raw = append([]byte(nil), raw...)
	return &b, nil
}

// Read loads private material without modifying the author directory. Validate
// against the portable source before using the bundle.
func Read(dir string) (*Bundle, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "instructor.yaml"))
	if os.IsNotExist(err) {
		if _, e := os.Lstat(filepath.Join(dir, "instructor")); !os.IsNotExist(e) {
			return nil, invalid("instructor.yaml", "instructor programs require a manifest")
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(filepath.Join(dir, "instructor.yaml"))
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, invalid("instructor.yaml", "expected a regular file")
	}
	b, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	total := 0
	err = filepath.WalkDir(filepath.Join(dir, "instructor"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(filepath.Join(dir, "instructor"), path)
		if d.IsDir() {
			if rel != "." && strings.HasPrefix(d.Name(), ".") {
				return invalid("files", "hidden instructor directory")
			}
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !safePath(rel) || d.Type()&os.ModeSymlink != 0 {
			return invalid("files", "unsafe instructor file "+rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return invalid("files", "expected bounded regular instructor files")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		total += len(data)
		if total > 1<<20 || len(b.Files) >= 64 {
			return invalid("files", "instructor programs exceed 64 files / 1 MiB")
		}
		b.Files = append(b.Files, lc.File{Path: rel, Content: data})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return b, nil
}

// LoadTemplate excludes private material before loading portable source. README
// rules are opt-in so ordinary CLI source validation needs no hosted handover.
func LoadTemplate(dir string, hostedTemplate ...bool) (lc.Source, *Bundle, error) {
	b, err := Read(dir)
	if err != nil {
		return lc.Source{}, nil, err
	}
	src, err := lc.LoadWithOptions(dir, lc.LoadOptions{Exclude: []string{"instructor.yaml", "instructor"}})
	if err != nil {
		return src, b, err
	}
	if len(hostedTemplate) > 0 && hostedTemplate[0] {
		info, e := os.Lstat(filepath.Join(dir, "README.md"))
		if e != nil || !info.Mode().IsRegular() {
			return src, b, invalid("README.md", "hosted templates require a regular README.md")
		}
	}
	if b != nil {
		err = b.Validate(src)
	}
	return src, b, err
}
func index(field string, i int) string { return fmt.Sprintf("%s[%d]", field, i) }
