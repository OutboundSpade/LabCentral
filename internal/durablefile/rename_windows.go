package durablefile

import "golang.org/x/sys/windows"

// Windows does not support fsync on a directory opened with os.Open. Request
// synchronous publication rather than reporting an error after the rename.
func Rename(source, destination string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
