package hosted

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Rebuild with LABCENTRAL_REGENERATE_V2_FIXTURE=1 go test ./hosted
// -run '^TestRetainedV2FixturePublicProvenance$'. No external source is read.
// Legacy files payloads and guest-login instructions are intentionally omitted:
// historical revisions permit reads and identity-based cleanup, never execution.
func publicRetainedV2Fixture(t *testing.T) []byte {
	t.Helper()
	type object = map[string]any
	toObject := func(value any) object {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var out object
		if err = json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	root := filepath.Join(t.TempDir(), "template")
	if err := os.CopyFS(root, os.DirFS("../templates/ccdc-service-recovery")); err != nil {
		t.Fatal(err)
	}
	b, err := extractInstructor(root)
	if err != nil {
		t.Fatal(err)
	}
	src, err := lc.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Validate(src); err != nil {
		t.Fatal(err)
	}
	const at = "2026-01-01T12:00:00Z"
	const later = "2026-01-01T16:00:00Z"
	objects := []object{}
	put := func(kind, id string, body any) {
		objects = append(objects, object{"kind": kind, "id": id, "body": body})
	}
	legacyStages := []object{}
	steps := []object{}
	for _, stage := range b.Stages {
		x := toObject(stage)
		delete(x, "completion")
		delete(x, "break")
		x["kind"], x["checkID"], x["exerciseID"] = stage.Kind(), stage.Completion.Check, stage.Break
		x["answer"] = ""
		if stage.Completion.Answer != nil {
			x["answer"] = stage.Completion.Answer.Accepted[0]
		}
		legacyStages = append(legacyStages, x)
		step := toObject(x)
		delete(step, "exerciseID")
		steps = append(steps, step)
	}
	legacy := toObject(b)
	legacy["version"], legacy["stages"], legacy["exercises"], legacy["files"] = 2, legacyStages, b.Breaks, []any{}
	delete(legacy, "breaks")
	delete(legacy, "visibility")
	delete(legacy, "practice")
	scenarios := []object{}
	for _, scenario := range b.Scenarios {
		x := toObject(scenario)
		variants := []object{}
		for _, variant := range scenario.Variants {
			variants = append(variants, object{"id": variant.ID, "compatible": variant.Compatible, "exerciseIDs": variant.Breaks, "stageIDs": variant.Stages})
		}
		x["variants"] = variants
		scenarios = append(scenarios, x)
	}
	legacy["scenarios"] = scenarios
	// Keep public prose, excluding the entire paragraph containing guest login instructions.
	handover, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	paragraphs := []string{}
	for _, paragraph := range strings.Split(string(handover), "\n\n") {
		if !strings.HasPrefix(paragraph, "Open your workstation desktop.") {
			paragraphs = append(paragraphs, paragraph)
		}
	}
	publicHandover := strings.Join(paragraphs, "\n\n")
	env := object{"version": 2, "name": "synthetic-upgrade", "platform": "aws", "operatorCIDRs": []string{"192.0.2.1/32"}, "bases": object{"ubuntu-24.04": object{"id": "ami-example", "identity": "ami-example", "user": "ubuntu", "diskGiB": 8}}, "aws": object{"account": "123456789012", "region": "us-east-1", "zone": "us-east-1a", "types": []object{{"name": "t3.medium", "cpu": 2, "memoryMiB": 4096}}}}
	env["build"] = object{"packer": "/synthetic/packer", "packerSHA256": strings.Repeat("a", 64), "pluginDirectory": "/synthetic/plugins", "toolchainID": "synthetic-toolchain", "ansibleVersion": "2.19.3", "timeoutSeconds": 3600}
	env["aws"].(object)["desktopProfile"] = "synthetic-license-profile"
	images := []object{}
	sources := map[string]object{}
	for _, name := range []string{"branch-services", "branch-services-ir", "ccdc-service-recovery", "retained-instructor-v1"} {
		id := "retained-" + name
		if name == "retained-instructor-v1" {
			id = name
		}
		lab := toObject(src.Lab)
		lab["version"], lab["name"] = 2, name
		// v2 stored images used a top-level playbook rather than build.
		oldImages := object{}
		for imageName, image := range src.Lab.Images {
			oldImages[imageName] = object{"base": image.Base, "playbook": image.Build.Playbook}
		}
		lab["images"] = oldImages
		source := object{"lab": lab, "digest": digest("synthetic-v2-source:" + name), "files": []any{}}
		sources[id] = source
		bundle := toObject(legacy)
		view := object{"notes": b.Notes, "checks": b.Checks, "exercises": b.Breaks, "stages": legacyStages}
		if name == "branch-services-ir" {
			bundle["audience"], bundle["hiddenMachines"] = "coaches", []string{"services"}
			view["audience"], view["hiddenMachines"] = "coaches", []string{"services"}
		}
		if name == "retained-instructor-v1" {
			bundle["version"], bundle["stages"], view["stages"] = 1, []any{}, []any{}
		}
		keys := []string{}
		for _, imageName := range []string{"services", "workstation"} {
			key := digest("synthetic-v2-image:" + name + ":" + imageName)
			keys = append(keys, key)
			images = append(images, object{"key": key, "name": imageName, "id": "image-" + name + "-" + imageName, "environment": env, "environmentDigest": digest("synthetic-environment"), "created": at, "resource": object{"kind": "image", "id": "ami-" + name + "-" + imageName, "name": imageName, "owner": "build-" + name, "complete": true}})
		}
		put("source", id, source)
		put("template-instructor", id, bundle)
		put("template-images", id, keys)
		put("template", id, object{"id": id, "name": name, "commit": strings.Repeat("c", 40), "commitTitle": "Synthetic retained public service-recovery revision", "committedAt": at, "importedAt": at, "built": true, "handover": publicHandover, "instructor": view})
		put("template-head", name, id)
	}
	put("upgrade-environment", "fixture", env)
	put("upgrade-images", "fixture", images)
	put("meeting", "current", object{"id": "v2-meeting", "name": "Team practice", "template": "retained-branch-services", "status": "ready", "created": at, "expires": later})
	teams := []object{}
	jobs := []object{}
	job := func(id, kind, target, status string) {
		jobs = append(jobs, object{"id": id, "kind": kind, "target": target, "status": status, "error": "", "created": at})
	}
	instance := func(id string) {
		put("instance", id, object{"id": id, "name": "Retained example allocation", "status": "ready", "created": at, "plan": object{"version": 2, "source": sources["retained-branch-services"], "environment": env, "environmentDigest": digest("synthetic-environment"), "images": []any{}, "machines": []object{{"name": "services", "spec": src.Lab.Machines["services"]}, {"name": "workstation", "spec": src.Lab.Machines["workstation"]}}}, "resources": []object{{"kind": "machine", "name": "services", "id": "native-" + id, "owner": id, "complete": true}}})
	}
	for _, team := range []string{"team-1", "team-2"} {
		id := "allocation-" + team
		teams = append(teams, object{"id": team, "meeting_id": "v2-meeting", "name": team, "capacity": 6, "instance_id": id, "status": "ready"})
		instance(id)
		results := []object{}
		for _, check := range b.Checks {
			results = append(results, object{"id": check.ID, "name": check.Name, "points": check.Points, "passed": true, "status": "passed", "report": "Synthetic confirmed check"})
		}
		checkID := "check-" + team
		put("check", checkID, object{"id": checkID, "meetingID": "v2-meeting", "teamID": team, "started": at, "updated": at, "status": "complete", "results": results})
		put("check-current", team, checkID)
		job("job-"+checkID, "check", checkID, "complete")
	}
	put("check-schedule", "v2-meeting", object{"meetingID": "v2-meeting", "intervalSeconds": 60, "next": later})
	put("exercise", "exercise-1", object{"id": "exercise-1", "meetingID": "v2-meeting", "teamID": "team-1", "action": "stop-web", "status": "complete", "report": "Synthetic retained exercise", "requestedBy": "student", "started": at, "updated": at})
	put("exercise-current", "team-1", "exercise-1")
	job("job-exercise", "exercise", "exercise-1", "complete")
	publication := func(id, owner, scenarioID, mode string, selected []object) {
		put("learning-publication", id, object{"id": id, "template": "retained-ccdc-service-recovery", "title": "Example Branch practice", "description": src.Lab.Description, "mode": mode, "estimatedMinutes": 30, "steps": selected, "published": true, "publishedAt": at, "releasedSteps": len(selected), "ownerID": owner, "scenarioID": scenarioID})
		put("learning-progress", progressKey("student", id), object{"labID": id, "completedSteps": 1, "updated": at, "lastFeedback": "Synthetic saved progress"})
	}
	for _, requestID := range []string{strings.Repeat("a", 24), strings.Repeat("b", 24)} {
		id := digest("attempt\x00student\x00" + requestID)[:24]
		scenarioID, variantID := "", ""
		selected, exerciseIDs := steps[:3], []string{}
		var summary *TemplateScenarioSummary
		if requestID[0] == 'b' {
			scenarioID, variantID = b.Scenarios[0].ID, "combined"
			selected, exerciseIDs = steps[3:], []string{"stop-web", "stop-scheduler"}
			s := scenarioSummary(b.Scenarios[0])
			summary = &s
		}
		publication(id, "student", scenarioID, "self-paced", selected)
		put("template-attempt", id, object{"id": id, "userID": "student", "template": "retained-ccdc-service-recovery", "requestID": requestID, "scenarioID": scenarioID, "variantID": variantID, "exerciseIDs": exerciseIDs, "scenario": summary, "bundle": legacy})
	}
	legacySteps := []object{toObject(steps[0]), toObject(steps[3])}
	legacySteps[1]["id"] = "restore"
	publication("legacy-lesson", "", "", "instructor", legacySteps)
	check := object{"id": "v2-learning-check", "labID": "legacy-lesson", "stepID": "restore", "status": "passed", "report": "Synthetic retained verification", "updated": at, "userID": "student", "runID": "practice-active", "completedSteps": 1}
	put("learning-check", "v2-learning-check", check)
	put("learning-check-current", progressKey("student", "legacy-lesson"), check)
	practice := []object{}
	for _, run := range []struct{ id, user, status string }{{"practice-ended", "student", "ended"}, {"practice-active", "student", "ready"}, {"practice-pending", "pending-student", "ending"}} {
		allocation := "allocation-" + run.id
		ended := ""
		if run.status == "ended" {
			ended = at
		} else {
			instance(allocation)
		}
		practice = append(practice, object{"id": run.id, "user_id": run.user, "lab_id": "legacy-lesson", "status": run.status, "instance_id": allocation, "created": at, "expires": later, "ended": ended, "error": ""})
		job("job-prepare-"+run.id, "practice-prepare", run.id, "complete")
		if run.status == "ending" {
			job("job-end-"+run.id, "practice-end", run.id, "queued")
		}
		if run.status == "ended" {
			job("job-end-"+run.id, "practice-end", run.id, "complete")
		}
	}
	put("user", "student", object{"id": "student", "name": "Student", "email": "student@example.invalid", "admin": true})
	fixture := object{"objects": objects, "teams": teams, "memberships": []object{{"user_id": "student", "team_id": "team-1", "pending_team": "", "switching": 0}}, "jobs": jobs, "practice_runs": practice}
	raw, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}

func TestRetainedV2FixturePublicProvenance(t *testing.T) {
	want := publicRetainedV2Fixture(t)
	const path = "testdata/retained-v2.json.gz"
	if os.Getenv("LABCENTRAL_REGENERATE_V2_FIXTURE") == "1" {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		z := gzip.NewWriter(f)
		if _, err = z.Write(want); err != nil {
			t.Fatal(err)
		}
		if err = z.Close(); err != nil {
			t.Fatal(err)
		}
		if err = f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	got, err := io.ReadAll(z)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("retained fixture differs from public source and synthetic records; regenerate it")
	}
}
