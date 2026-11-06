//go:build windows

package attachments

import "os"

// readFlags is unused on Windows: supported reports false and Open refuses
// before any file is touched.
const readFlags = os.O_RDONLY

func supported() bool { return false }

func identity(os.FileInfo) (dev, ino uint64, ok bool) { return 0, 0, false }
