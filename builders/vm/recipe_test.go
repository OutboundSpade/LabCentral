package vm

import (
	"strings"
	"testing"
)

func TestDockerfilePinsEveryImageSource(t *testing.T) {
	digest := "sha256:" + strings.Repeat("1", 64)
	image := "registry.example.com/base@" + digest
	for _, source := range []string{
		"FROM " + image + " AS base\nRUN true\nFROM base\nCOPY --from=0 /bin/x /bin/x\n",
		"FROM scratch\nCOPY hello /hello\n",
		"FROM shared\nRUN true\n",
	} {
		if err := ValidateDockerfile([]byte(source), []string{digest}, []string{"shared"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, source := range []string{
		"# syntax=docker/dockerfile:1\nFROM scratch\n",
		"ARG BASE=" + image + "\nFROM ${BASE}\n",
		"FROM alpine:latest\n",
		"FROM scratch\nCOPY --from=alpine:latest /bin/x /bin/x\n",
		"FROM " + image + "\nRUN --mount=from=alpine:latest,target=/mnt true\n",
		"FROM scratch\nADD https://example.com/file /file\n",
		"FROM --platform=linux/arm64 " + image + "\n",
		"FROM scratch AS shared\n",
	} {
		if err := ValidateDockerfile([]byte(source), []string{digest}, []string{"shared"}); err == nil {
			t.Fatalf("unpinned or unsupported source accepted: %s", source)
		}
	}
}
func TestContextRejectsTraversalAndDuplicateFiles(t *testing.T) {
	for _, value := range []string{
		`{"version":1,"files":[{"path":"../escape","data":""}]}`,
		`{"version":1,"files":[{"path":"Dockerfile","data":""},{"path":"Dockerfile","data":""}]}`,
		`{"version":1,"files":[{"path":"/root/x","data":""}]}`,
		`{"version":1,"files":[{"path":"a\\b","data":""}]}`,
		`{"version":1,"files":[{"path":"Dockerfile","data":""}],"secret":"hidden"}`,
	} {
		if _, err := ParseContext([]byte(value)); err == nil {
			t.Fatalf("unsafe context accepted: %s", value)
		}
	}
}
