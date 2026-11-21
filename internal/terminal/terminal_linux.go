package terminal

import "syscall"

const readTermios = syscall.TCGETS
const writeTermios = syscall.TCSETS

func selectInput(n int, ready *syscall.FdSet, timeout *syscall.Timeval) error {
	_, err := syscall.Select(n, ready, nil, nil, timeout)
	return err
}
