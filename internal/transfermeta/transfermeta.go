package transfermeta

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path"
	"strings"
)

// The workbench accepts at most 2 GB per file. The same cap applies while the
// metadata scrubber stages a recognized file for format-aware rewriting.
const MaxBytes int64 = 2 << 30

var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

type format int

const (
	formatUnknown format = iota
	formatJPEG
	formatPNG
	formatGIF
	formatWebP
	formatOOXML
	formatODF
	formatWAV
	formatBlocked
)

// Prepared is a host-side file body ready for the guest. Body is either the
// original stream or a private temporary file with metadata removed. Close
// releases any temporary file; it is safe to call more than once.
type Prepared struct {
	Body    io.Reader
	Size    int64
	cleanup func() error
}

func (p *Prepared) Close() error {
	if p == nil || p.cleanup == nil {
		return nil
	}
	return p.cleanup()
}

// Prepare inspects the stream and removes supported metadata before it reaches
// the guest. Unsupported media/containers and malformed supported files fail
// closed. Ordinary non-media files pass through. The size hint
// may be -1 when the caller does not know the content length.
func Prepare(name string, src io.Reader, hint int64) (*Prepared, error) {
	if src == nil {
		return nil, errors.New("文件内容为空")
	}
	reader := bufio.NewReaderSize(src, 64<<10)
	head, err := reader.Peek(1024)
	if err != nil && err != io.EOF && err != bufio.ErrBufferFull {
		return nil, err
	}
	kind := detectFormat(name, head)
	if kind == formatBlocked {
		return nil, ErrUnsupported
	}
	if kind == formatUnknown {
		return &Prepared{Body: reader, Size: hint}, nil
	}
	source, size, err := stageSource(reader)
	if err != nil {
		return nil, err
	}
	out, err := os.CreateTemp("", "cage-transfer-output-*")
	if err != nil {
		_ = closeAndRemove(source)
		return nil, err
	}
	changed, err := sanitize(kind, name, source, out, size)
	if err != nil {
		_ = closeAndRemove(out)
		_ = closeAndRemove(source)
		return nil, errors.Join(ErrMalformed, err)
	}
	if !changed {
		_ = out.Close()
		_ = os.Remove(out.Name())
		_, _ = source.Seek(0, 0)
		return &Prepared{Body: source, Size: size, cleanup: func() error { return closeAndRemove(source) }}, nil
	}
	if err := out.Sync(); err != nil {
		_ = closeAndRemove(source)
		_ = closeAndRemove(out)
		return nil, err
	}
	if _, err := out.Seek(0, 0); err != nil {
		_ = closeAndRemove(source)
		_ = closeAndRemove(out)
		return nil, err
	}
	info, err := out.Stat()
	if err != nil {
		_ = closeAndRemove(source)
		_ = closeAndRemove(out)
		return nil, err
	}
	_ = closeAndRemove(source)
	return &Prepared{Body: out, Size: info.Size(), cleanup: func() error { return closeAndRemove(out) }}, nil
}

// SanitizeBytes applies the same format-aware scrub to an in-memory image or
// document. It returns the original slice when no metadata was found.
func SanitizeBytes(name string, data []byte) ([]byte, error) {
	kind := detectFormat(name, data)
	if kind == formatBlocked {
		return nil, ErrUnsupported
	}
	if kind == formatUnknown {
		return data, nil
	}
	var out bytes.Buffer
	changed, err := sanitizeBytes(kind, name, data, &out)
	if err != nil {
		return nil, errors.Join(ErrMalformed, err)
	}
	if !changed {
		return data, nil
	}
	return out.Bytes(), nil
}

func detectFormat(name string, head []byte) format {
	if len(head) >= 3 && head[0] == 0xFF && head[1] == 0xD8 && head[2] == 0xFF {
		return formatJPEG
	}
	if bytes.HasPrefix(head, pngSignature) {
		return formatPNG
	}
	if bytes.HasPrefix(head, []byte("GIF87a")) || bytes.HasPrefix(head, []byte("GIF89a")) {
		return formatGIF
	}
	if len(head) >= 12 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "WEBP" {
		return formatWebP
	}
	if len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WAVE" {
		return formatWAV
	}
	if blockedFormat(name, head) {
		return formatBlocked
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".docx", ".docm", ".xlsx", ".xlsm", ".pptx", ".pptm":
		return formatOOXML
	case ".odt", ".ods", ".odp", ".odg", ".odf":
		return formatODF
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".wav":
		return formatBlocked // a supported extension with an invalid signature
	}
	return formatUnknown
}

func stageSource(src io.Reader) (*os.File, int64, error) {
	file, err := os.CreateTemp("", "cage-transfer-source-*")
	if err != nil {
		return nil, 0, err
	}
	count, err := io.Copy(file, io.LimitReader(src, MaxBytes+1))
	if err != nil {
		_ = closeAndRemove(file)
		return nil, 0, err
	}
	if count > MaxBytes {
		_ = closeAndRemove(file)
		return nil, 0, errors.New("每个文件不能超过 2 GB")
	}
	if err := file.Sync(); err != nil {
		_ = closeAndRemove(file)
		return nil, 0, err
	}
	if _, err := file.Seek(0, 0); err != nil {
		_ = closeAndRemove(file)
		return nil, 0, err
	}
	return file, count, nil
}

func closeAndRemove(file *os.File) error {
	if file == nil {
		return nil
	}
	name := file.Name()
	err := file.Close()
	if removeErr := os.Remove(name); removeErr != nil && !os.IsNotExist(removeErr) {
		err = errors.Join(err, removeErr)
	}
	return err
}

func sanitize(kind format, name string, src *os.File, dst io.Writer, size int64) (bool, error) {
	switch kind {
	case formatJPEG:
		return sanitizeJPEG(src, dst)
	case formatPNG:
		return sanitizePNG(src, dst)
	case formatGIF:
		return sanitizeGIF(src, dst)
	case formatWebP:
		return sanitizeWebP(src, dst, size)
	case formatOOXML, formatODF:
		return sanitizeZipDoc(name, src, dst, size, kind)
	case formatWAV:
		return sanitizeWAV(src, dst, size)
	}
	return false, nil
}

func sanitizeBytes(kind format, name string, data []byte, dst io.Writer) (bool, error) {
	switch kind {
	case formatJPEG:
		return sanitizeJPEG(bytes.NewReader(data), dst)
	case formatPNG:
		return sanitizePNG(bytes.NewReader(data), dst)
	case formatGIF:
		return sanitizeGIF(bytes.NewReader(data), dst)
	case formatWebP:
		return sanitizeWebP(bytes.NewReader(data), dst, int64(len(data)))
	case formatOOXML, formatODF:
		return sanitizeZipBytes(name, data, dst, kind)
	case formatWAV:
		return sanitizeWAV(bytes.NewReader(data), dst, int64(len(data)))
	}
	return false, nil
}
