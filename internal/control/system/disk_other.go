//go:build !linux

package system

import "errors"

func diskCapacity(string) (Capacity, error) {
	return Capacity{}, errors.New("disk metrics require Linux")
}
