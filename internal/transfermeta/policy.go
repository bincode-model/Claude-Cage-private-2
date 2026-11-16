package transfermeta

import (
	"bytes"
	"errors"
	"path"
	"strings"
)

var (
	ErrUnsupported = errors.New("此媒体或容器格式暂不能可靠清除元数据，请先在本机转换为支持的格式")
	ErrMalformed   = errors.New("文件结构异常，无法确认元数据已清除，已停止传递")
)

// Check both signatures and names: renaming a PDF, TIFF, MP4 or ZIP must not
// turn it into an ordinary pass-through file. Office ZIPs are handled below.
func blockedFormat(name string, head []byte) bool {
	ext := strings.ToLower(path.Ext(name))
	if len(head) >= 262 && string(head[257:262]) == "ustar" {
		return true
	}
	if bytes.HasPrefix(bytes.TrimSpace(head), []byte("%PDF-")) {
		return true
	}
	if bytes.HasPrefix(head, []byte("PK\x03\x04")) || bytes.HasPrefix(head, []byte("PK\x05\x06")) || bytes.HasPrefix(head, []byte("PK\x07\x08")) {
		switch ext {
		case ".docx", ".docm", ".xlsx", ".xlsm", ".pptx", ".pptm", ".odt", ".ods", ".odp", ".odg", ".odf":
			return false
		}
		return true
	}
	for _, sig := range [][]byte{
		[]byte("%PDF-"), {'I', 'I', 42, 0}, {'M', 'M', 0, 42}, {'I', 'I', 43, 0}, {'M', 'M', 0, 43},
		[]byte("8BPS"), []byte("icns"), []byte("BM"), {0, 0, 1, 0}, []byte("%!"),
		[]byte("ID3"), []byte("fLaC"), []byte("OggS"), []byte("RIFF"), []byte("RF64"), []byte("FORM"),
		[]byte("Rar!"), {'7', 'z', 0xbc, 0xaf, 0x27, 0x1c}, {0x1f, 0x8b}, []byte("BZh"),
		{0xfd, '7', 'z', 'X', 'Z', 0}, {0x28, 0xb5, 0x2f, 0xfd}, {0x1a, 0x45, 0xdf, 0xa3},
		{0x30, 0x26, 0xb2, 0x75}, {0xd0, 0xcf, 0x11, 0xe0}, []byte("FLV"),
	} {
		if bytes.HasPrefix(head, sig) {
			return true
		}
	}
	if len(head) >= 8 {
		switch string(head[4:8]) {
		case "ftyp", "moov", "mdat", "wide":
			return true
		}
	}
	if len(head) >= 2 && head[0] == 0xff && head[1]&0xe0 == 0xe0 {
		return true
	} // MPEG/ADTS
	switch ext {
	case ".pdf", ".tif", ".tiff", ".heic", ".heif", ".hif", ".avif", ".avifs", ".jxl", ".jp2", ".psd", ".dng", ".raw", ".cr2", ".cr3", ".nef", ".arw", ".svg", ".svgz", ".bmp", ".ico", ".icns", ".ps", ".eps", ".xps", ".oxps",
		".mp3", ".mp2", ".aac", ".m4a", ".m4b", ".flac", ".ogg", ".opus", ".aif", ".aiff", ".wma", ".ape", ".caf", ".mid", ".midi", ".amr",
		".mp4", ".m4v", ".mov", ".avi", ".mkv", ".mka", ".webm", ".wmv", ".mpg", ".mpeg", ".mts", ".m2ts", ".3gp", ".flv", ".vob",
		".zip", ".tar", ".tgz", ".gz", ".bz2", ".xz", ".zst", ".7z", ".rar", ".iso", ".epub", ".doc", ".xls", ".ppt", ".rtf":
		return true
	}
	return false
}
