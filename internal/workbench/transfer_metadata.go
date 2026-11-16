package workbench

import (
	"errors"
	"net/http"
	"os"

	"github.com/sylvinus/agent-vm/internal/transfermeta"
)

func transferMetadataError(w http.ResponseWriter, err error) {
	if errors.Is(err, transfermeta.ErrUnsupported) {
		http.Error(w, transfermeta.ErrUnsupported.Error(), http.StatusUnprocessableEntity)
	} else if errors.Is(err, transfermeta.ErrMalformed) {
		http.Error(w, transfermeta.ErrMalformed.Error(), http.StatusUnprocessableEntity)
	} else {
		http.Error(w, "文件元数据清理失败，请重试", http.StatusInternalServerError)
	}
}

// The SFTP server owns src and any temporary replacement returned here.
func sharedReadView(name string, src *os.File) (*os.File, error) {
	fi, err := src.Stat()
	if err != nil {
		return nil, err
	}
	prepared, err := transfermeta.Prepare(name, src, fi.Size())
	if err != nil {
		return nil, err
	}
	if file, ok := prepared.Body.(*os.File); ok {
		return file, nil
	}
	// Prepare only buffered an ordinary pass-through file. SFTP uses ReadAt.
	_, err = src.Seek(0, 0)
	return src, err
}
