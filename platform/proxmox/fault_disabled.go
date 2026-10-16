//go:build !faultinject

package proxmox

func faultError(err error, method, path string) error { return err }
