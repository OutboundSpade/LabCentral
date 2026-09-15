package containervm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClosedBootstrapAndLiteralArgv(t *testing.T) {
	c := Config{SchemaVersion: Version, InstallationID: "installation.test", InstanceID: "instance.test", ResourceID: "container.web", Generation: 1, Image: "docker.io/library/python@sha256:" + strings.Repeat("a", 64), Environment: map[string]string{"VALUE": "$(touch /tmp/escape)\n--privileged"}, Command: []string{"python3", "-m", "http.server"}, Args: []string{"--privileged"}}
	data, _ := json.Marshal(c)
	if _, err := Decode(data); err != nil {
		t.Fatal(err)
	}
	args := c.PodmanCreateArgs("binding")
	if args[len(args)-2] != c.Image || args[len(args)-1] != "--privileged" {
		t.Fatal("author args became engine flags", args)
	}
	if _, err := Decode(append(data, []byte(" {}")...)); err == nil {
		t.Fatal("trailing data accepted")
	}
	for _, bad := range []string{strings.Replace(string(data), `"schemaVersion":`, `"unknown":true,"schemaVersion":`, 1), strings.Replace(string(data), "@sha256:", ":latest@sha1:", 1)} {
		if _, err := Decode([]byte(bad)); err == nil {
			t.Fatal("invalid bootstrap accepted")
		}
	}
}
