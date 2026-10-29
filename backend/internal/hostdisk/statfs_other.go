//go:build !linux

package hostdisk

import "errors"

func statfs(string) (Space, error) { return Space{}, errors.ErrUnsupported }
