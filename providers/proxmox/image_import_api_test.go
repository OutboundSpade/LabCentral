package proxmox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
			var order []string
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
				order = append(order, part.FormName())
				if part.FormName() == "filename" && part.FileName() != filename {
					t.Error("wrong owned filename")
				}
			}
			if strings.Join(order, ",") != "content,checksum-algorithm,checksum,filename" {
				t.Error("multipart field order is incompatible with Proxmox's streaming parser", order)
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

func TestImageStagingCleanupWaitsForAbsenceAndRecovers(t *testing.T) {
	filename := "lc-image-" + strings.Repeat("b", 64) + ".qcow2"
	for _, delayed := range []bool{true, false} {
		t.Run(fmt.Sprint(delayed), func(t *testing.T) {
			var deletes, reads atomic.Int32
			var absent atomic.Bool
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "DELETE" {
					deletes.Add(1)
					if !strings.HasSuffix(r.URL.Path, "/local:import/"+filename) {
						t.Error("cleanup targeted another volume")
					}
					writeAPIData(t, w, "UPID:pve-a:00000001:00000002:00000003:imgdel:local:labcentral@pve!discovery:")
					return
				}
				count := reads.Add(1)
				if absent.Load() || (delayed && count >= 3) {
					writeAPIData(t, w, []any{})
					return
				}
				writeAPIData(t, w, []map[string]string{{"volid": "local:import/" + filename}})
			}))
			defer server.Close()
			client := newTestClient(t, server, 0)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			if !delayed {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
			}
			defer cancel()
			err := client.DeleteImageStaging(ctx, "pve-a", "local", filename)
			if delayed && (err != nil || reads.Load() < 3) {
				t.Fatal("reported cleanup before observed absence", err)
			}
			if !delayed && err == nil {
				t.Fatal("reported cleanup while volume remains")
			}
			if deletes.Load() != 1 {
				t.Fatal("unexpected repeated deletion", deletes.Load())
			}
			absent.Store(true)
			if err := client.DeleteImageStaging(context.Background(), "pve-a", "local", filename); err != nil {
				t.Fatal(err)
			}
			if deletes.Load() != 1 {
				t.Fatal("recovery repeated completed deletion")
			}
		})
	}
}
