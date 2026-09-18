package proxmox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestImageUploadAPIStreamsChecksumAndPollsTask(t *testing.T) {
	data := []byte("bounded-image-fixture")
	digest := bareDigest(data)
	filename := "lc-image-" + strings.Repeat("a", 64) + ".qcow2"
	taskRaw := "UPID:pve-a:00000001:00000002:00000003:imgcopy::labcentral@pve!discovery:"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			if r.URL.Path != "/api2/json/nodes/pve-a/storage/local/upload" || r.ContentLength <= int64(len(data)) {
				t.Error("unbounded upload request")
			}
			multipart, err := r.MultipartReader()
			if err != nil {
				t.Fatal(err)
			}
			fields := map[string]string{}
			for {
				part, err := multipart.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				raw, err := io.ReadAll(part)
				if err != nil {
					t.Fatal(err)
				}
				fields[part.FormName()] = string(raw)
				if part.FormName() == "filename" && part.FileName() != filename {
					t.Error("wrong owned filename")
				}
			}
			if fields["content"] != "import" || fields["checksum-algorithm"] != "sha256" || fields["checksum"] != digest || fields["filename"] != string(data) {
				t.Fatal("multipart identity mismatch", fields)
			}
			writeAPIData(t, w, taskRaw)
		} else {
			if !strings.HasSuffix(r.URL.Path, "/status") {
				t.Fatal(r.URL.Path)
			}
			writeAPIData(t, w, map[string]string{"status": "stopped", "exitstatus": "OK"})
		}
	}))
	defer server.Close()
	client := newTestClient(t, server, 0)
	task, err := client.UploadImage(context.Background(), ImageUpload{Node: "pve-a", Storage: "local", Filename: filename, Digest: digest, Size: uint64(len(data))}, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.TaskStatus(context.Background(), "pve-a", task); err != nil {
		t.Fatal(err)
	}
	if _, err = client.UploadImage(context.Background(), ImageUpload{Node: "pve-a", Storage: "local", Filename: "../../foreign", Digest: digest, Size: 1}, bytes.NewReader(data)); err == nil {
		t.Fatal("accepted external filename")
	}
}
func TestRecoverUploadUsesUniqueFilenameAndVerifiedChecksum(t *testing.T) {
	filename := "lc-image-" + strings.Repeat("a", 64) + ".qcow2"
	verified := false
	taskRaw := "UPID:pve-a:00000001:00000002:00000003:imgcopy::labcentral@pve!discovery:"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tasks") {
			if r.URL.Query().Get("source") != "all" {
				t.Fatal("not recovering active tasks")
			}
			writeAPIData(t, w, []map[string]string{{"upid": taskRaw}})
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/log") {
			t.Fatal(r.URL.Path)
		}
		lines := []map[string]string{{"t": "target file: /var/lib/vz/import/" + filename}}
		if verified {
			lines = append(lines, map[string]string{"t": "calculating checksum...OK, checksum verified"})
		}
		writeAPIData(t, w, lines)
	}))
	defer server.Close()
	client := newTestClient(t, server, 0)
	if _, found, err := client.RecoverImageTask(context.Background(), "pve-a", "imgcopy", 200, filename); err != nil || found {
		t.Fatal("accepted missing checksum", found, err)
	}
	verified = true
	task, found, err := client.RecoverImageTask(context.Background(), "pve-a", "imgcopy", 200, filename)
	if err != nil || !found || task.Raw != taskRaw {
		t.Fatal(fmt.Sprint(task, found, err))
	}
}
