//go:build linux || darwin || windows

package reversesshfs

import (
	"errors"
	"io"
	"os"

	"github.com/pkg/sftp"
)

type readFile interface {
	io.ReaderAt
	io.Closer
	Stat() (os.FileInfo, error)
}

type viewFileInfo struct {
	os.FileInfo
	size int64
}

func (fi viewFileInfo) Size() int64 { return fi.size }

type filteredRead struct {
	*os.File
	source *os.File
	info   os.FileInfo
}

func (f *filteredRead) Stat() (os.FileInfo, error) {
	fi, err := f.File.Stat()
	if err != nil {
		return nil, err
	}
	return viewFileInfo{f.info, fi.Size()}, nil
}

func (f *filteredRead) Close() error {
	return errors.Join(f.File.Close(), os.Remove(f.File.Name()), f.source.Close())
}

func (h *rootedHandlers) filterRead(name string, src *os.File) (readFile, error) {
	if h.readTransform == nil {
		return src, nil
	}
	fi, err := src.Stat()
	if err != nil {
		src.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return src, nil
	}
	out, err := h.readTransform(name, src)
	if err != nil {
		src.Close()
		return nil, err
	}
	if out == nil {
		src.Close()
		return nil, errors.New("file read transform returned no file")
	}
	if out == src {
		return src, nil
	}
	return &filteredRead{File: out, source: src, info: fi}, nil
}

func (h *rootedHandlers) viewInfo(name string, fi os.FileInfo) os.FileInfo {
	if h.readTransform == nil || !fi.Mode().IsRegular() {
		return fi
	}
	src, err := h.openRead(name)
	if err != nil {
		return fi
	}
	// A concurrent rename must not give an old directory entry a new size.
	current, err := src.Stat()
	if err != nil || !os.SameFile(fi, current) {
		src.Close()
		return fi
	}
	view, err := h.filterRead(name, src)
	if err != nil {
		return fi
	} // listing remains possible; OPEN still fails closed
	defer view.Close()
	info, err := view.Stat()
	if err != nil {
		return fi
	}
	return viewFileInfo{fi, info.Size()}
}

// Recognized media uses a private editable view. Ordinary project files keep
// their original live handles. Opening a changed media file O_RDWR does not
// mutate the host; writes are committed to the already guarded host handle on
// CLOSE. This keeps guest byte offsets relative to the cleaned representation.
type filteredWritable struct {
	*filteredRead
	appends bool
	dirty   bool
}

func (f *filteredWritable) WriteAt(p []byte, off int64) (int, error) {
	if f.appends {
		fi, err := f.File.Stat()
		if err != nil {
			return 0, err
		}
		off = fi.Size()
	}
	n, err := f.File.WriteAt(p, off)
	if n > 0 {
		f.dirty = true
	}
	return n, err
}

func (f *filteredWritable) setstat(h *rootedHandlers, r *sftp.Request) error {
	if r.AttrFlags().Size {
		if len(r.Attrs) < 8 {
			return sftp.ErrSSHFxBadMessage
		}
		if err := f.File.Truncate(int64(r.Attributes().Size)); err != nil {
			return err
		}
		f.dirty = true
		copy := sftp.NewRequest("Setstat", r.Filepath)
		copy.Flags = r.Flags &^ 1 // SSH_FILEXFER_ATTR_SIZE is the leading eight bytes
		copy.Attrs = r.Attrs[8:]
		return f.setSourceStat(h, copy)
	}
	return f.setSourceStat(h, r)
}

func (f *filteredWritable) setSourceStat(h *rootedHandlers, r *sftp.Request) error {
	if err := f.checkSource(); err != nil {
		return err
	}
	if err := h.fsetstat(f.source, r); err != nil {
		return err
	}
	fi, err := f.source.Stat()
	if err == nil {
		f.info = fi
	}
	return err
}

func (f *filteredWritable) checkSource() error {
	current, err := f.source.Stat()
	if err != nil {
		return err
	}
	if current.Size() != f.info.Size() || !current.ModTime().Equal(f.info.ModTime()) {
		return errors.New("shared source changed while editing; writeback refused")
	}
	return nil
}

func (f *filteredWritable) Close() error {
	var err error
	if f.dirty {
		if err := f.checkSource(); err != nil {
			return errors.Join(err, f.filteredRead.Close())
		}
		if _, err = f.File.Seek(0, io.SeekStart); err == nil {
			err = f.source.Truncate(0)
		}
		if err == nil {
			_, err = f.source.Seek(0, io.SeekStart)
		}
		if err == nil {
			_, err = io.Copy(f.source, f.File)
		}
		if err == nil {
			err = f.source.Sync()
		}
	}
	return errors.Join(err, f.filteredRead.Close())
}
