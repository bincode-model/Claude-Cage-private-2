package reversesshfs

import (
	"io"
	"os"
)

// ReadTransform returns src unchanged or a private, seekable temporary file.
// It must not close src. The server owns both handles and removes a temporary
// replacement on close. Errors deny the read; they never fall back to src.
type ReadTransform func(name string, src *os.File) (*os.File, error)

// ServeConfined serves only localPath over an already secured transport. It uses
// the same root and metadata protections as builtin reverse-sshfs mounts.
func ServeConfined(transport io.ReadWriteCloser, localPath string, readonly bool, readonlyNames []string, transform ...ReadTransform) error {
	server, root, err := newRootedServer(transport, localPath, readonly, readonlyNames)
	if err != nil {
		return err
	}
	defer root.Close()
	defer server.Close()
	if len(transform) > 0 {
		root.readTransform = transform[0]
	}
	return server.Serve()
}

// ServeConfinedVirtual exposes the selected directory at / without disclosing
// its host path. All reads, writes and Guard decisions use the same confined
// root and metadata protections as ServeConfined.
func ServeConfinedVirtual(transport io.ReadWriteCloser, localPath string, readonly bool, readonlyNames []string, transform ...ReadTransform) error {
	server, root, err := newRootedServerAt(transport, localPath, "/", readonly, readonlyNames)
	if err != nil {
		return err
	}
	defer root.Close()
	defer server.Close()
	if len(transform) > 0 {
		root.readTransform = transform[0]
	}
	return server.Serve()
}
