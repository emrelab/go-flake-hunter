package terminal

import "syscall"

const readTermios = syscall.TIOCGETA
const writeTermios = syscall.TIOCSETA

func selectInput(n int, ready *syscall.FdSet, timeout *syscall.Timeval) error {
	return syscall.Select(n, ready, nil, nil, timeout)
}
