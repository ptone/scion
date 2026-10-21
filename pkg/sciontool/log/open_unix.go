//go:build unix

/*
Copyright 2025 The Scion Authors.
*/

package log

import (
	"fmt"
	"os"
	"syscall"

	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/dirfd"
)

func openLogFileNoFollow(path string, mode os.FileMode) (*os.File, error) {
	dirFd, leaf, err := dirfd.OpenParentNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dirfd.Close(dirFd) }()

	f, err := dirfd.OpenAt(dirFd, leaf,
		syscall.O_APPEND|syscall.O_CREAT|syscall.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, mode)
	if err != nil {
		return nil, err
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		_ = f.Close()
		return nil, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Nlink != 1 {
		_ = f.Close()
		return nil, fmt.Errorf("log path %s is not a single-link regular file", path)
	}
	return f, nil
}
