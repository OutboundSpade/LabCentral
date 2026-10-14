package instructor

import (
	"encoding/json"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"path"
	"regexp"
	"slices"
	"strings"
)

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)
var extensionPattern = regexp.MustCompile(`^[a-z0-9]+(\.[a-z0-9-]+)+$`)
var filePattern = regexp.MustCompile(`^[a-zA-Z0-9_./-]+$`)

func safePath(s string) bool {
	if s == "" || !filePattern.MatchString(s) || path.IsAbs(s) || path.Clean(s) != s {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}
func cleanName(s string) bool {
	return strings.TrimSpace(s) != "" && len(s) <= 120 && !strings.ContainsAny(s, "\x00\r\n")
}
func difficulty(s string) bool {
	return lc.Contains([]string{"beginner", "intermediate", "advanced"}, s)
}
func (b Bundle) Validate(src lc.Source) error {
	err := b.validate(src)
	if problem, ok := err.(*lc.Error); ok && (strings.HasPrefix(problem.Field, "images.") || strings.HasPrefix(problem.Field, "machines.")) {
		for _, file := range src.Files {
			if file.Path == "lab.yaml" {
				return lc.YAMLLineError(file.Content, err)
			}
		}
	}
	if err != nil && len(b.raw) > 0 {
		return lc.YAMLLineError(b.raw, err)
	}
	return err
}
func (b Bundle) validate(src lc.Source) error {
	if b.Version != 3 {
		return invalid("version", "expected 3")
	}
	if !lc.Contains([]string{"students", "coaches"}, b.Visibility.Audience) {
		return invalid("visibility.audience", "expected students or coaches")
	}
	if len(b.Notes) > 32000 {
		return invalid("notes", "exceeds 32000 bytes")
	}
	for _, item := range []struct {
		field       string
		length, max int
	}{{"checks", len(b.Checks), 20}, {"breaks", len(b.Breaks), 20}, {"stages", len(b.Stages), 20}, {"scenarios", len(b.Scenarios), 12}} {
		if item.length > item.max {
			return invalid(item.field, "too many definitions")
		}
	}
	if err := b.validateVisibility(src); err != nil {
		return err
	}
	if len(b.Stages) > 0 || len(b.Scenarios) > 0 {
		if b.Practice == nil {
			return invalid("practice.desktopMachine", "required for stages or scenarios")
		}
	}
	if b.Practice != nil {
		m, ok := src.Lab.Machines[b.Practice.DesktopMachine]
		if !ok || !lc.Contains(m.Access, "desktop") || lc.Contains(b.Visibility.HiddenMachines, b.Practice.DesktopMachine) {
			return invalid("practice.desktopMachine", "requires a visible declared machine with desktop access")
		}
	}
	total := 0
	ext := func(field string, x lc.Extensions) error {
		keys := []string{}
		for k := range x {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			if !extensionPattern.MatchString(k) || !json.Valid(x[k]) {
				return invalid(field+"."+k, "expected namespaced JSON extension")
			}
		}
		if x != nil {
			data, _ := json.Marshal(x)
			total += len(data)
		}
		if total > 64<<10 {
			return invalid(field, "total instructor extensions exceed 64 KiB")
		}
		return nil
	}
	if err := ext("extensions", b.Extensions); err != nil {
		return err
	}
	files := map[string]bool{}
	bytes := 0
	if len(b.Files) > 64 {
		return invalid("files", "too many instructor files")
	}
	for i, f := range b.Files {
		field := index("files", i)
		if !safePath(f.Path) || strings.HasPrefix(f.Path, "template/") || strings.HasPrefix(f.Path, "labcentral/") || f.Path == "template" || f.Path == "labcentral" || files[f.Path] {
			return invalid(field, "unsafe, reserved or duplicate instructor path")
		}
		files[f.Path] = true
		bytes += len(f.Content)
	}
	if bytes > 1<<20 {
		return invalid("files", "instructor programs exceed 1 MiB")
	}
	steps := func(field string, steps []Step, extensions lc.Extensions) error {
		if len(steps) < 1 || len(steps) > 8 {
			return invalid(field, "expected 1–8 steps")
		}
		for i, step := range steps {
			if err := b.validateStep(src, step, index(field, i), extensions); err != nil {
				return err
			}
		}
		return nil
	}
	if len(b.Breaks) > 0 || len(b.Reset) > 0 {
		if err := steps("reset", b.Reset, nil); err != nil {
			return err
		}
	}
	seen := map[string]bool{"reset": true, "check": true}
	checks, breaks := map[string]bool{}, map[string]bool{}
	definition := func(field, id, name string) error {
		if !idPattern.MatchString(id) || seen[id] {
			return invalid(field+".id", "expected unique lowercase ID")
		}
		seen[id] = true
		if !cleanName(name) {
			return invalid(field+".name", "expected bounded nonempty name")
		}
		return nil
	}
	for i, c := range b.Checks {
		f := index("checks", i)
		if err := definition(f, c.ID, c.Name); err != nil {
			return err
		}
		if c.Points < 1 || c.Points > 100 {
			return invalid(f+".points", "expected 1–100")
		}
		if err := ext(f+".extensions", c.Extensions); err != nil {
			return err
		}
		if err := steps(f+".steps", c.Steps, c.Extensions); err != nil {
			return err
		}
		checks[c.ID] = true
	}
	for i, x := range b.Breaks {
		f := index("breaks", i)
		if err := definition(f, x.ID, x.Name); err != nil {
			return err
		}
		if len(x.Description) > 6000 {
			return invalid(f+".description", "exceeds 6000 bytes")
		}
		if len(x.Walkthrough) > 32000 {
			return invalid(f+".walkthrough", "exceeds 32000 bytes")
		}
		if err := ext(f+".extensions", x.Extensions); err != nil {
			return err
		}
		if err := steps(f+".steps", x.Steps, x.Extensions); err != nil {
			return err
		}
		breaks[x.ID] = true
	}
	stageIDs := map[string]Stage{}
	for i, st := range b.Stages {
		f := index("stages", i)
		if !idPattern.MatchString(st.ID) {
			return invalid(f+".id", "invalid ID")
		}
		if _, ok := stageIDs[st.ID]; ok {
			return invalid(f+".id", "duplicate ID")
		}
		stageIDs[st.ID] = st
		if !cleanName(st.Title) {
			return invalid(f+".title", "expected bounded nonempty title")
		}
		if strings.TrimSpace(st.Instructions) == "" || len(st.Instructions) > 6000 {
			return invalid(f+".instructions", "expected 1–6000 bytes")
		}
		if len(st.Prompt) > 1000 {
			return invalid(f+".prompt", "exceeds 1000 bytes")
		}
		if len(st.Solution) > 6000 {
			return invalid(f+".solution", "exceeds 6000 bytes")
		}
		if st.Break != "" && !breaks[st.Break] {
			return invalid(f+".break", "unknown break")
		}
		c := st.Completion
		count := 0
		if c.Answer != nil {
			count++
		}
		if c.Check != "" {
			count++
		}
		if c.Instructor != nil {
			count++
		}
		if count != 1 {
			return invalid(f+".completion", "expected exactly one of answer, check or instructor")
		}
		if c.Check != "" && !checks[c.Check] {
			return invalid(f+".completion.check", "unknown check")
		}
		if c.Instructor != nil && !*c.Instructor {
			return invalid(f+".completion.instructor", "expected true")
		}
		if c.Answer != nil {
			if len(c.Answer.Accepted) < 1 || len(c.Answer.Accepted) > 10 {
				return invalid(f+".completion.answer.accepted", "expected 1–10 values")
			}
			for j, a := range c.Answer.Accepted {
				if strings.TrimSpace(a) == "" || len(a) > 1000 {
					return invalid(index(f+".completion.answer.accepted", j), "expected 1–1000 bytes")
				}
			}
		}
		if err := ext(f+".extensions", st.Extensions); err != nil {
			return err
		}
	}
	if b.Library != nil {
		m := b.Library
		if !cleanName(m.Category) || len(m.Category) > 80 {
			return invalid("library.category", "expected 1–80 bytes")
		}
		if !difficulty(m.Difficulty) {
			return invalid("library.difficulty", "invalid difficulty")
		}
		if m.EstimatedMinutes < 1 || m.EstimatedMinutes > 1440 {
			return invalid("library.estimatedMinutes", "expected 1–1440")
		}
		if len(m.Tags) > 12 {
			return invalid("library.tags", "at most 12 tags")
		}
		seen := map[string]bool{}
		for i, t := range m.Tags {
			if strings.TrimSpace(t) == "" || len(t) > 48 || seen[t] {
				return invalid(index("library.tags", i), "expected unique 1–48 byte tags")
			}
			seen[t] = true
		}
	}
	if len(b.Scenarios) > 0 && (b.Visibility.Audience == "coaches" || len(b.Reset) == 0) {
		return invalid("scenarios", "student scenarios require reset")
	}
	seen = map[string]bool{}
	for i, sc := range b.Scenarios {
		f := index("scenarios", i)
		if !idPattern.MatchString(sc.ID) || seen[sc.ID] {
			return invalid(f+".id", "expected unique ID")
		}
		seen[sc.ID] = true
		if !cleanName(sc.Title) {
			return invalid(f+".title", "invalid title")
		}
		if strings.TrimSpace(sc.Description) == "" || len(sc.Description) > 2000 {
			return invalid(f+".description", "expected 1–2000 bytes")
		}
		if !difficulty(sc.Difficulty) {
			return invalid(f+".difficulty", "invalid difficulty")
		}
		if sc.EstimatedMinutes < 1 || sc.EstimatedMinutes > 1440 {
			return invalid(f+".estimatedMinutes", "expected 1–1440")
		}
		if len(sc.Variants) < 2 || len(sc.Variants) > 32 {
			return invalid(f+".variants", "expected 2–32 variants")
		}
		variants, combinations := map[string]bool{}, map[string]bool{}
		for j, v := range sc.Variants {
			vf := index(f+".variants", j)
			if !idPattern.MatchString(v.ID) || variants[v.ID] {
				return invalid(vf+".id", "expected unique ID")
			}
			variants[v.ID] = true
			if !v.Compatible {
				return invalid(vf+".compatible", "explicit true required")
			}
			if len(v.Breaks) < 1 || len(v.Breaks) > 8 {
				return invalid(vf+".breaks", "expected 1–8 breaks")
			}
			if len(v.Stages) < 1 || len(v.Stages) > 20 {
				return invalid(vf+".stages", "expected 1–20 stages")
			}
			selected := map[string]bool{}
			for k, id := range v.Breaks {
				if !breaks[id] || selected[id] {
					return invalid(index(vf+".breaks", k), "unknown or duplicate break")
				}
				selected[id] = true
			}
			normalized := slices.Clone(v.Breaks)
			slices.Sort(normalized)
			key := strings.Join(normalized, "\x00")
			if combinations[key] {
				return invalid(vf+".breaks", "variants require distinct break sets")
			}
			combinations[key] = true
			used, covered := map[string]bool{}, map[string]bool{}
			for k, id := range v.Stages {
				st, ok := stageIDs[id]
				if !ok || used[id] || st.Kind() == "instructor" || (st.Break != "" && !selected[st.Break]) {
					return invalid(index(vf+".stages", k), "requires unique self-paced stages matching selected breaks")
				}
				used[id] = true
				if st.Kind() == "action" && st.Break != "" {
					covered[st.Break] = true
				}
			}
			for k, id := range v.Breaks {
				if !covered[id] {
					return invalid(index(vf+".breaks", k), "break needs an action recovery stage")
				}
			}
		}
	}
	return nil
}
func (b Bundle) validateVisibility(src lc.Source) error {
	hidden := b.Visibility.HiddenMachines
	seen := map[string]bool{}
	if len(hidden) > 0 && b.Visibility.Audience != "coaches" {
		return invalid("visibility.hiddenMachines", "hidden machines require coaches audience")
	}
	for i, n := range hidden {
		if _, ok := src.Lab.Machines[n]; !ok || seen[n] {
			return invalid(index("visibility.hiddenMachines", i), "unknown or duplicate hidden machine")
		}
		seen[n] = true
	}
	names := []string{}
	for n := range src.Lab.Machines {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		m := src.Lab.Machines[n]
		if len(hidden) == 0 || seen[n] {
			continue
		}
		im := src.Lab.Images[m.Image]
		f := "images." + m.Image + ".build.files"
		if len(im.BuildFiles()) == 0 || lc.Contains(im.BuildFiles(), "lab.yaml") {
			return invalid(f, "visible images require explicit files excluding lab.yaml")
		}
		for _, h := range hidden {
			private := src.Lab.Images[src.Lab.Machines[h].Image].BuildPlaybook()
			if private != "" && private != im.BuildPlaybook() && lc.Contains(im.BuildFiles(), private) {
				return invalid(f, "private image playbook reaches visible image")
			}
		}
		if !m.HostnamePeers.Explicit {
			return invalid("machines."+n+".hostnamePeers", "visible guests require explicit peers")
		}
		for i, p := range m.HostnamePeers.Names {
			if seen[p] {
				return invalid(index("machines."+n+".hostnamePeers", i), "hidden identity reaches visible guest")
			}
		}
	}
	return nil
}
func (b Bundle) validateStep(src lc.Source, s Step, f string, extensions lc.Extensions) error {
	if _, ok := src.Lab.Machines[s.Machine]; !ok {
		return invalid(f+".machine", "unknown machine")
	}
	if s.TimeoutSeconds != 0 && (s.TimeoutSeconds < 10 || s.TimeoutSeconds > 600) {
		return invalid(f+".timeoutSeconds", "expected 10–600")
	}
	if len(s.Files) > 64 {
		return invalid(f+".files", "at most 64 files")
	}
	if len(s.Assets) > 20 {
		return invalid(f+".assets", "at most 20 assets")
	}
	private := map[string]lc.File{}
	for _, file := range b.Files {
		private[file.Path] = file
	}
	portable := map[string]lc.File{}
	for _, file := range src.Files {
		portable[file.Path] = file
	}
	destinations := map[string]bool{"labcentral/hosts.json": true, "labcentral/context.json": true}
	payload := []lc.File{}
	payloadFields := map[string]string{}
	add := func(dest string, file lc.File, field string) error {
		if destinations[dest] {
			return invalid(field, "duplicate payload destination")
		}
		for existing := range destinations {
			if strings.HasPrefix(existing, dest+"/") || strings.HasPrefix(dest, existing+"/") {
				return invalid(field, "payload file/directory collision")
			}
		}
		destinations[dest] = true
		payloadFields[dest] = field
		file.Path = dest
		payload = append(payload, file)
		return nil
	}
	if !safePath(s.Playbook) || strings.HasPrefix(s.Playbook, "template/") || strings.HasPrefix(s.Playbook, "labcentral/") {
		return invalid(f+".playbook", "unsafe or reserved path")
	}
	file, ok := private[s.Playbook]
	if !ok {
		return invalid(f+".playbook", "missing instructor playbook")
	}
	if err := add(s.Playbook, file, f+".playbook"); err != nil {
		return err
	}
	for i, p := range s.Files {
		field := index(f+".files", i)
		if !safePath(p) || strings.HasPrefix(p, "template/") || strings.HasPrefix(p, "labcentral/") {
			return invalid(field, "unsafe or reserved path")
		}
		file, ok := private[p]
		if !ok {
			return invalid(field, "missing instructor file")
		}
		if err := add(p, file, field); err != nil {
			return err
		}
	}
	bytes := 0
	for i, p := range s.Assets {
		field := index(f+".assets", i)
		if !safePath(p) {
			return invalid(field, "unsafe asset path")
		}
		file, ok := portable[p]
		if !ok {
			return invalid(field, "missing portable asset")
		}
		bytes += len(file.Content)
		if bytes > 1<<20 {
			return invalid(field, "assets exceed 1 MiB")
		}
		if err := add("template/"+p, file, field); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for i, n := range s.GuestHosts {
		field := index(f+".guestHosts", i)
		if _, ok := src.Lab.Machines[n]; !ok || seen[n] {
			return invalid(field, "unknown or duplicate guest identity")
		}
		seen[n] = true
		if !lc.Contains(b.Visibility.HiddenMachines, s.Machine) && lc.Contains(b.Visibility.HiddenMachines, n) {
			return invalid(field, "hidden identity reaches visible guest")
		}
	}
	if len(b.Visibility.HiddenMachines) > 0 && !lc.Contains(b.Visibility.HiddenMachines, s.Machine) {
		// Build files used only by hidden machines' images are private; files shared
		// with a visible machine's image already reach students.
		privateImageFiles := map[string]bool{}
		for _, h := range b.Visibility.HiddenMachines {
			im := src.Lab.Images[src.Lab.Machines[h].Image]
			for _, p := range append([]string{im.BuildPlaybook()}, im.BuildFiles()...) {
				if p != "" {
					privateImageFiles[p] = true
				}
			}
		}
		for name, m := range src.Lab.Machines {
			if !lc.Contains(b.Visibility.HiddenMachines, name) {
				for _, p := range src.Lab.Images[m.Image].BuildFiles() {
					delete(privateImageFiles, p)
				}
			}
		}
		context, _ := json.Marshal(GuestContext{s.Machine, src.Lab.Extensions, b.Extensions, extensions})
		payload = append(payload, lc.File{Path: "labcentral/context.json", Content: context})
		for _, file := range payload {
			if file.Path == "template/lab.yaml" {
				return invalid(payloadFields[file.Path], "lab.yaml reaches visible guest")
			}
			if privateImageFiles[strings.TrimPrefix(file.Path, "template/")] && strings.HasPrefix(file.Path, "template/") {
				return invalid(payloadFields[file.Path], "private image file reaches visible guest")
			}
		}
	}
	return nil
}
