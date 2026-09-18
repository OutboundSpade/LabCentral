package ocilayout

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
)

func fixture(t *testing.T, osName string, mutate func(map[string][]byte)) ([]byte, Identity) {
	t.Helper()
	files := map[string][]byte{"oci-layout": []byte(`{"imageLayoutVersion":"1.0.0"}`)}
	add := func(value any) (string, int) {
		data, _ := json.Marshal(value)
		sum := sha256.Sum256(data)
		digest := hex.EncodeToString(sum[:])
		files["blobs/sha256/"+digest] = data
		return "sha256:" + digest, len(data)
	}
	config, size := add(map[string]any{"architecture": "amd64", "os": osName, "rootfs": map[string]any{"type": "layers", "diff_ids": []string{}}})
	manifest, msize := add(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": config, "size": size}, "layers": []any{}})
	files["index.json"] = []byte(fmt.Sprintf(`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q,"size":%d}]}`, manifest, msize))
	if mutate != nil {
		mutate(files)
	}
	var out bytes.Buffer
	w := tar.NewWriter(&out)
	for name, data := range files {
		if err := w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		w.Write(data)
	}
	w.Close()
	return out.Bytes(), Identity{ManifestDigest: manifest, ConfigDigest: config, Architecture: "amd64", OS: osName}
}
func TestIdentityVerifiesCompleteOCIClosure(t *testing.T) {
	data, want := fixture(t, "linux", nil)
	got, err := Inspect(bytes.NewReader(data), uint64(len(data)))
	if err != nil || got != want {
		t.Fatal(got, err)
	}
	tests := map[string][]byte{"truncated": data[:len(data)/2], "extra-content": append(append([]byte{}, data...), []byte("hidden")...)}
	tests["foreign-os"], _ = fixture(t, "windows", nil)
	tests["corrupt-blob"], _ = fixture(t, "linux", func(files map[string][]byte) {
		for name := range files {
			if len(name) > 13 && name[:13] == "blobs/sha256/" {
				files[name] = []byte(`{}`)
				break
			}
		}
	})
	tests["missing-config"], _ = fixture(t, "linux", func(files map[string][]byte) {
		for name, data := range files {
			if bytes.Contains(data, []byte(`"rootfs"`)) {
				delete(files, name)
				break
			}
		}
	})
	tests["escape"], _ = fixture(t, "linux", func(files map[string][]byte) { files["../escape"] = []byte("bad") })
	tests["external-image"], _ = fixture(t, "linux", func(files map[string][]byte) {
		var index map[string]any
		json.Unmarshal(files["index.json"], &index)
		index["manifests"].([]any)[0].(map[string]any)["urls"] = []string{"https://untrusted.invalid/blob"}
		files["index.json"], _ = json.Marshal(index)
	})
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Inspect(bytes.NewReader(data), uint64(len(data))); err == nil {
				t.Fatal("accepted invalid OCI")
			}
		})
	}
	if _, err := Inspect(bytes.NewReader(data), uint64(len(data)-1)); err == nil {
		t.Fatal("ignored byte bound")
	}
}
