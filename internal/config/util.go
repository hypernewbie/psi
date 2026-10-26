package config

import "bytes"

func stripBOM(b []byte) []byte { return bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")) }

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
