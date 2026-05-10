package proxyfs

import (
	"errors"
	"io/fs"
)

var ErrConflict = errors.New("proxyfs: target path conflict")

func pathError(op, name string, err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*fs.PathError); ok {
		return err
	}
	return &fs.PathError{Op: op, Path: name, Err: err}
}
