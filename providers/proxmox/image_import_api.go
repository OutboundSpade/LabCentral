package proxmox

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var imageFilenamePattern = regexp.MustCompile(`^lc-image-[0-9a-f]{64}\.qcow2$`)

// ImageImportAPI is discovered independently of ordinary VM clone support.
// Author bytes are uploaded to bounded import storage; no URL or host path is
// accepted from source content.
type ImageImportAPI interface {
	MutationAPI
	RecoverImageTask(context.Context, string, string, uint32, string) (UPID, bool, error)
	ImportStorage(context.Context, string, string) (bool, error)
	UploadImage(context.Context, ImageUpload, io.Reader) (UPID, error)
	CreateImageVM(context.Context, ImageVMCreate) (UPID, error)
	MakeImageTemplate(context.Context, string, uint32) (UPID, error)
	DeleteImageStaging(context.Context, string, string, string) error
}
type ImageUpload struct {
	Node, Storage, Filename, Digest string
	Size                            uint64
}
type ImageVMCreate struct {
	Node, Pool, Name, Description, Storage, Format, StagingStorage, Filename string
	VMID                                                                     uint32
}

func (client *Client) ImportStorage(ctx context.Context, node, storage string) (bool, error) {
	if !nodeNamePattern.MatchString(node) || !storageIDPattern.MatchString(storage) {
		return false, fmt.Errorf("invalid import storage")
	}
	type storageStatus struct {
		Content string  `json:"content"`
		Active  apiBool `json:"active"`
		Enabled apiBool `json:"enabled"`
		Type    string  `json:"type"`
	}
	s, err := getAPI[storageStatus](ctx, client, "/nodes/"+node+"/storage/"+storage+"/status", nil)
	if err != nil {
		return false, err
	}
	return bool(s.Active) && bool(s.Enabled) && contains(strings.Split(s.Content, ","), "import") && (s.Type == "dir" || s.Type == "nfs" || s.Type == "cifs"), nil
}
func (client *Client) UploadImage(ctx context.Context, r ImageUpload, src io.Reader) (UPID, error) {
	if !nodeNamePattern.MatchString(r.Node) || !storageIDPattern.MatchString(r.Storage) || !imageFilenamePattern.MatchString(r.Filename) || !sha256Pattern.MatchString("sha256:"+r.Digest) || r.Size == 0 || r.Size > 32<<30 {
		return UPID{}, fmt.Errorf("invalid image upload")
	}
	// Construct a small multipart prefix/suffix and stream the exact bounded body.
	// Content-Length avoids unbounded buffering by intermediate HTTP handlers.
	var prefix bytes.Buffer
	writer := multipart.NewWriter(&prefix)
	// Proxmox's streaming multipart parser consumes these fields in this order.
	// In particular checksum-algorithm must precede checksum and the file part.
	for _, v := range [][2]string{{"content", "import"}, {"checksum-algorithm", "sha256"}, {"checksum", r.Digest}} {
		if err := writer.WriteField(v[0], v[1]); err != nil {
			return UPID{}, err
		}
	}
	if _, err := writer.CreateFormFile("filename", r.Filename); err != nil {
		return UPID{}, err
	}
	head := append([]byte(nil), prefix.Bytes()...)
	prefix.Reset()
	if err := writer.Close(); err != nil {
		return UPID{}, err
	}
	tail := append([]byte(nil), prefix.Bytes()...)
	u := *client.endpoint
	u.Path = "/api2/json/nodes/" + r.Node + "/storage/" + r.Storage + "/upload"
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), io.MultiReader(bytes.NewReader(head), io.LimitReader(src, int64(r.Size)), bytes.NewReader(tail)))
	if err != nil {
		return UPID{}, err
	}
	req.ContentLength = int64(len(head)) + int64(r.Size) + int64(len(tail))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "PVEAPIToken="+client.tokenID+"="+client.tokenSecret)
	h := *client.httpClient
	h.Timeout = 15 * time.Minute
	response, err := h.Do(req)
	if err != nil {
		return UPID{}, fmt.Errorf("image upload transport failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return UPID{}, &APIStatusError{Method: "POST", Path: u.Path, StatusCode: response.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, client.maxResponseBytes+1))
	if err != nil || int64(len(raw)) > client.maxResponseBytes {
		return UPID{}, fmt.Errorf("invalid upload response")
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return UPID{}, err
	}
	var envelope struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return UPID{}, err
	}
	task, err := ParseUPID(envelope.Data)
	if err == nil {
		err = task.validate(r.Node, "imgcopy")
	}
	return task, err
}
func (client *Client) CreateImageVM(ctx context.Context, r ImageVMCreate) (UPID, error) {
	if !nodeNamePattern.MatchString(r.Node) || !storageIDPattern.MatchString(r.Pool) || !vmNamePattern.MatchString(r.Name) || !strings.HasPrefix(r.Description, "LCIMAGE1:") || len(r.Description) > 2048 || !storageIDPattern.MatchString(r.Storage) || !storageIDPattern.MatchString(r.StagingStorage) || !imageFilenamePattern.MatchString(r.Filename) || r.VMID < 100 || r.VMID > 999999999 || (r.Format != "raw" && r.Format != "qcow2") {
		return UPID{}, fmt.Errorf("invalid image VM request")
	}
	p := url.Values{"vmid": {strconv.FormatUint(uint64(r.VMID), 10)}, "name": {r.Name}, "pool": {r.Pool}, "description": {r.Description}, "ostype": {"l26"}, "sockets": {"1"}, "cores": {"1"}, "memory": {"512"}, "agent": {"enabled=1"}, "scsihw": {"virtio-scsi-single"}, "scsi0": {r.Storage + ":0,import-from=" + r.StagingStorage + ":import/" + r.Filename + ",format=" + r.Format}, "ide2": {r.Storage + ":cloudinit"}, "boot": {"order=scsi0"}, "ciupgrade": {"0"}, "onboot": {"0"}}
	task, err := client.taskRequest(ctx, "/nodes/"+r.Node+"/qemu", p)
	if err == nil {
		err = task.validateObject(r.Node, r.VMID, "qmcreate")
	}
	return task, err
}
func (client *Client) MakeImageTemplate(ctx context.Context, node string, vmid uint32) (UPID, error) {
	if !nodeNamePattern.MatchString(node) || vmid < 100 || vmid > 999999999 {
		return UPID{}, fmt.Errorf("invalid template VM")
	}
	task, err := client.taskRequest(ctx, fmt.Sprintf("/nodes/%s/qemu/%d/template", node, vmid), nil)
	if err == nil {
		err = task.validateObject(node, vmid, "qmtemplate")
	}
	return task, err
}
func (client *Client) DeleteImageStaging(ctx context.Context, node, storage, filename string) error {
	if !nodeNamePattern.MatchString(node) || !storageIDPattern.MatchString(storage) || !imageFilenamePattern.MatchString(filename) {
		return fmt.Errorf("invalid staging identity")
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	exists := func() (bool, error) {
		type contentItem struct {
			Volume string `json:"volid"`
		}
		items, err := getAPI[[]contentItem](ctx, client, "/nodes/"+node+"/storage/"+storage+"/content", url.Values{"content": {"import"}})
		if err != nil {
			return false, err
		}
		if len(items) > 4096 {
			return false, fmt.Errorf("staging inventory exceeds bound")
		}
		for _, item := range items {
			if item.Volume == storage+":import/"+filename {
				return true, nil
			}
		}
		return false, nil
	}
	found, err := exists()
	if err != nil || !found {
		return err
	}
	// PVE deletion is asynchronous. Its accepted task is not absence evidence.
	// Recovery lists this invocation's immutable name before attempting cleanup.
	_, err = requestAPIWithNull[json.RawMessage](ctx, client, "DELETE", "/nodes/"+node+"/storage/"+storage+"/content/"+storage+":import/"+filename, nil, true)
	if err != nil {
		return err
	}
	for {
		found, err = exists()
		if err != nil || !found {
			return err
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}

}

// inspectStandaloneQCOW reads only the fixed header, without invoking a parser
// on the privileged provider host. PVE additionally validates untrusted images.
func inspectStandaloneQCOW(r io.Reader, maximum uint64) (uint64, error) {
	h := make([]byte, 112)
	if _, err := io.ReadFull(r, h); err != nil {
		return 0, err
	}
	u32 := func(n int) uint32 { return binary.BigEndian.Uint32(h[n : n+4]) }
	u64 := func(n int) uint64 { return binary.BigEndian.Uint64(h[n : n+8]) }
	version := u32(4)
	if string(h[:4]) != "QFI\xfb" || (version != 2 && version != 3) || u64(8) != 0 || u32(16) != 0 || u32(20) < 9 || u32(20) > 21 || u32(32) != 0 || u32(60) != 0 || u64(64) != 0 || u64(24) == 0 || u64(24) > maximum {
		return 0, fmt.Errorf("unsupported standalone qcow2 header")
	}
	if version == 3 && (u64(72) != 0 || (u32(100) != 104 && u32(100) != 112) || u32(100) == 112 && u64(104) != 0) {
		return 0, fmt.Errorf("unsupported qcow2 incompatible features")
	}
	return u64(24), nil
}

// RecoverImageTask requires exact VM identity or the uniquely owned upload
// filename in the task log. Ambiguous/lost history never authorizes a replay.
func (client *Client) RecoverImageTask(ctx context.Context, node, kind string, vmid uint32, filename string) (UPID, bool, error) {
	if !nodeNamePattern.MatchString(node) || (kind != "imgcopy" && kind != "qmcreate" && kind != "qmtemplate") || (kind == "imgcopy" && !imageFilenamePattern.MatchString(filename)) {
		return UPID{}, false, fmt.Errorf("invalid import task identity")
	}
	q := url.Values{"typefilter": {kind}, "userfilter": {client.tokenID}, "source": {"all"}, "limit": {"128"}}
	if kind != "imgcopy" {
		q.Set("vmid", strconv.FormatUint(uint64(vmid), 10))
	}
	type taskEntry struct {
		UPID string `json:"upid"`
	}
	tasks, err := getAPI[[]taskEntry](ctx, client, "/nodes/"+node+"/tasks", q)
	if err != nil {
		return UPID{}, false, err
	}
	if len(tasks) >= 128 {
		return UPID{}, false, fmt.Errorf("import task history exceeds recovery bound")
	}
	var found UPID
	for _, entry := range tasks {
		task, err := ParseUPID(entry.UPID)
		if err != nil || task.validate(node, kind) != nil || task.User != client.tokenID {
			continue
		}
		if kind != "imgcopy" {
			if task.ObjectID != strconv.FormatUint(uint64(vmid), 10) {
				continue
			}
		} else {
			type line struct {
				Text string `json:"t"`
			}
			lines, err := getAPI[[]line](ctx, client, "/nodes/"+node+"/tasks/"+task.Raw+"/log", url.Values{"limit": {"100"}})
			if err != nil {
				return UPID{}, false, err
			}
			match, checksum := false, false
			for _, line := range lines {
				if strings.HasPrefix(line.Text, "target file: ") && strings.HasSuffix(line.Text, "/"+filename) {
					match = true
				}
				if strings.Contains(line.Text, "OK, checksum verified") {
					checksum = true
				}
			}
			if !match || !checksum {
				continue
			}
		}
		if found.Raw != "" {
			return UPID{}, false, fmt.Errorf("ambiguous image import task history")
		}
		found = task
	}
	return found, found.Raw != "", nil
}
