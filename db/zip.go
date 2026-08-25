package db

import (
	"archive/zip"
	"bytes"
	"io"
)

const zipEntryName = "content"

// packZip compresses s into a zip archive (a single entry) and returns the
// binary archive bytes suitable for storage in a BLOB column.
func packZip(s string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(zipEntryName)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write([]byte(s)); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// unpackZip decompresses binary zip archive bytes back into the original
// string. Empty input returns an empty string.
func unpackZip(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return ""
	}
	if len(zr.File) == 0 {
		return ""
	}
	rc, err := zr.File[0].Open()
	if err != nil {
		return ""
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return ""
	}
	return string(b)
}
