package workbench

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lima-vm/sshocker/pkg/reversesshfs"
	"github.com/pkg/sftp"
	"github.com/sylvinus/agent-vm/internal/transfermeta"
)

func locationPNG(t *testing.T) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	payload := []byte("Location\x00GPS=31.23;language=zh-CN;timezone=Asia/Shanghai")
	chunk := make([]byte, len(payload)+12)
	binary.BigEndian.PutUint32(chunk, uint32(len(payload)))
	copy(chunk[4:], "tEXt")
	copy(chunk[8:], payload)
	binary.BigEndian.PutUint32(chunk[len(chunk)-4:], crc32.ChecksumIEEE(chunk[4:len(chunk)-4]))
	data := append([]byte{}, encoded.Bytes()[:encoded.Len()-12]...)
	data = append(data, chunk...)
	return append(data, encoded.Bytes()[encoded.Len()-12:]...)
}

func TestSharedMetadata(t *testing.T) {
	for _, virtual := range []bool{false, true} {
		name := "host-root"
		if virtual {
			name = "virtual-root"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			original := locationPNG(t)
			host := filepath.Join(root, "photo.png")
			if err := os.WriteFile(host, original, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "blocked.pdf"), []byte("%PDF-1.7 GPS=31.23"), 0600); err != nil {
				t.Fatal(err)
			}
			server, conn := net.Pipe()
			_ = server.SetDeadline(time.Now().Add(20 * time.Second))
			_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer server.Close()
				if virtual {
					_ = reversesshfs.ServeConfinedVirtual(server, root, false, nil, sharedReadView)
				} else {
					_ = reversesshfs.ServeConfined(server, root, false, nil, sharedReadView)
				}
			}()
			client, err := sftp.NewClientPipe(conn, conn)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.Close(); _ = conn.Close(); <-done }()
			base := filepath.ToSlash(root)
			if virtual {
				base = ""
			}
			path := base + "/photo.png"
			clean, err := transfermeta.SanitizeBytes("photo.png", original)
			if err != nil {
				t.Fatal(err)
			}
			for _, flags := range []int{os.O_RDONLY, os.O_RDWR} {
				f, err := client.OpenFile(path, flags)
				if err != nil {
					t.Fatal(err)
				}
				fi, err := f.Stat()
				if err != nil || fi.Size() != int64(len(clean)) {
					t.Fatalf("FSTAT size: %v", err)
				}
				data, err := io.ReadAll(f)
				if err != nil || !bytes.Equal(data, clean) {
					t.Fatalf("metadata bypass for flags %d: %v", flags, err)
				}
				part := make([]byte, 9)
				if _, err := f.ReadAt(part, 11); err != nil || !bytes.Equal(part, clean[11:20]) {
					t.Fatal("random read changed")
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			for _, stat := range []func(string) (os.FileInfo, error){client.Stat, client.Lstat} {
				fi, err := stat(path)
				if err != nil || fi.Size() != int64(len(clean)) {
					t.Fatal("path stat uses original size")
				}
			}
			entries, err := client.ReadDir(base + "/")
			if err != nil {
				t.Fatal(err)
			}
			for _, fi := range entries {
				if fi.Name() == "photo.png" && fi.Size() != int64(len(clean)) {
					t.Fatal("directory size is stale")
				}
			}
			if raw, err := os.ReadFile(host); err != nil || !bytes.Equal(raw, original) {
				t.Fatal("opening a view changed host")
			}
			for _, flags := range []int{os.O_RDONLY, os.O_RDWR} {
				if f, err := client.OpenFile(base+"/blocked.pdf", flags); err == nil {
					f.Close()
					t.Fatal("unsupported media escaped")
				}
			}
			f, err := client.OpenFile(path, os.O_RDWR)
			if err != nil {
				t.Fatal(err)
			}
			// No-op writes exercise the guest's clean offsets and host writeback.
			if _, err := f.WriteAt(clean[8:20], 8); err != nil {
				t.Fatal(err)
			}
			if err := f.Truncate(int64(len(clean))); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			if raw, err := os.ReadFile(host); err != nil || !bytes.Equal(raw, clean) {
				t.Fatal("read/write view did not write back clean bytes")
			}
			f, err = client.OpenFile(path, os.O_RDWR)
			if err != nil {
				t.Fatal(err)
			}
			external := append(append([]byte{}, original...), []byte("host update")...)
			if err := os.WriteFile(host, external, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteAt(clean[8:20], 8); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err == nil {
				t.Fatal("concurrent host edit was overwritten")
			}
			if raw, err := os.ReadFile(host); err != nil || !bytes.Equal(raw, external) {
				t.Fatal("host edit was lost")
			}
			if err := client.Truncate(path, int64(len(clean))); err != nil {
				t.Fatal(err)
			}
			if raw, err := os.ReadFile(host); err != nil || !bytes.Equal(raw, clean) {
				t.Fatal("path truncate used original offsets")
			}
			if err := exerciseProjectGitWrites(client, base); err != nil {
				t.Fatal(err)
			}
			f, err = client.OpenFile(base+"/created.png", os.O_CREATE|os.O_RDWR|os.O_TRUNC)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.Write(clean); err != nil {
				t.Fatal(err)
			}
			if err = f.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestClipboardMetadata(t *testing.T) {
	root := t.TempDir()
	data := locationPNG(t)
	file := filepath.Join(root, "photo.png")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := writeClipboardArchive(&archive, []string{root}); err != nil {
		t.Fatal(err)
	}
	r := tar.NewReader(&archive)
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		clean, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(clean, []byte("Shanghai")) || bytes.Contains(clean, []byte("GPS")) || h.Size != int64(len(clean)) {
			t.Fatal("native archive preserved metadata")
		}
		if _, err := png.Decode(bytes.NewReader(clean)); err != nil {
			t.Fatal(err)
		}
	}
	if raw, err := os.ReadFile(file); err != nil || !bytes.Equal(raw, data) {
		t.Fatal("clipboard changed host source")
	}
	if err := os.WriteFile(filepath.Join(root, "blocked.pdf"), []byte("%PDF-1.7 GPS=31.23"), 0600); err != nil {
		t.Fatal(err)
	}
	archive.Reset()
	if err := writeClipboardArchive(&archive, []string{root}); err == nil {
		t.Fatal("native clipboard passed unsupported document")
	}
}

func TestClipboardRejectsRawArchive(t *testing.T) {
	s := testServer(t)
	s.ready = true
	if err := os.MkdirAll(s.instanceDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.instanceDir(), "cage-running.json"), []byte(fmt.Sprintf(`{"pid":%d}`, os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	w := tar.NewWriter(&archive)
	data := []byte("%PDF-1.7 GPS=31.23;Asia/Shanghai")
	if err := w.WriteHeader(&tar.Header{Name: "renamed.bin", Mode: 0600, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(data)
	_ = w.Close()
	r := httptest.NewRequest("POST", "/api/clipboard/files", &archive)
	response := httptest.NewRecorder()
	s.clipboardArchive(response, r)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("raw archive reached guest import: %d", response.Code)
	}
}
