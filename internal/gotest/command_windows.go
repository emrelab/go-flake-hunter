package gotest

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var jobKernel = syscall.NewLazyDLL("kernel32.dll")
var createJob = jobKernel.NewProc("CreateJobObjectW")
var setJobInfo = jobKernel.NewProc("SetInformationJobObject")
var assignJob = jobKernel.NewProc("AssignProcessToJobObject")
var terminateJob = jobKernel.NewProc("TerminateJobObject")
var threadFirst = jobKernel.NewProc("Thread32First")
var threadNext = jobKernel.NewProc("Thread32Next")
var openThread = jobKernel.NewProc("OpenThread")
var resumeThread = jobKernel.NewProc("ResumeThread")

// A suspended start prevents descendants escaping before assignment to the job.
// Only the lifecycle is wrapped; os/exec still owns pipes, waiting and context delivery.
type windowsCommand struct {
	*exec.Cmd
	mu        sync.Mutex
	job       syscall.Handle
	terminate func(syscall.Handle) error
}

func commandContext(ctx context.Context, name string, args ...string) *windowsCommand {
	c := &windowsCommand{Cmd: exec.CommandContext(ctx, name, args...)}
	c.WaitDelay = time.Second
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x4} // CREATE_SUSPENDED
	c.terminate = func(job syscall.Handle) error { return jobCall(terminateJob, uintptr(job), 1) }
	c.Cancel = func() error {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.job == 0 {
			return syscall.EINVAL
		}
		err := c.terminate(c.job)
		// Closing the last non-inherited handle also kills the tree if termination fails.
		closeErr := c.closeJob()
		if closeErr != nil {
			return errors.Join(err, closeErr)
		}
		return nil
	}
	return c
}

//go:uintptrescapes
func jobCall(proc *syscall.LazyProc, args ...uintptr) error {
	ok, _, err := proc.Call(args...)
	if ok == 0 {
		return fmt.Errorf("%s: %w", proc.Name, err)
	}
	return nil
}

func (c *windowsCommand) closeJob() error {
	if c.job == 0 {
		return nil
	}
	if err := syscall.CloseHandle(c.job); err != nil {
		return err
	}
	c.job = 0
	return nil
}

func (c *windowsCommand) Start() error {
	handle, _, err := createJob.Call(0, 0)
	if handle == 0 {
		return fmt.Errorf("CreateJobObjectW: %w", err)
	}
	c.job = syscall.Handle(handle)
	// JOBOBJECT_EXTENDED_LIMIT_INFORMATION: 144 bytes on 64-bit, 112 on 32-bit.
	var limits [144]byte
	size := uintptr(len(limits))
	if unsafe.Sizeof(uintptr(0)) == 4 {
		size = 112
	}
	binary.LittleEndian.PutUint32(limits[16:20], 0x2000) // JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if err := jobCall(setJobInfo, handle, 9, uintptr(unsafe.Pointer(&limits)), size); err != nil {
		return errors.Join(err, c.closeJob())
	}
	if err := c.Cmd.Start(); err != nil {
		return errors.Join(err, c.closeJob())
	}
	c.mu.Lock()
	err = c.attachAndResume()
	c.mu.Unlock()
	if err != nil {
		killErr := c.Process.Kill()
		waitErr := c.Wait()
		return errors.Join(err, killErr, waitErr)
	}
	return nil
}

func (c *windowsCommand) attachAndResume() error {
	process, err := syscall.OpenProcess(0x100|0x1, false, uint32(c.Process.Pid)) // SET_QUOTA | TERMINATE
	if err != nil {
		return err
	}
	err = jobCall(assignJob, uintptr(c.job), uintptr(process))
	closeErr := syscall.CloseHandle(process)
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	snapshot, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer func() { _ = syscall.CloseHandle(snapshot) }()
	var entry struct {
		Size, Usage, ID, Owner      uint32
		BasePriority, DeltaPriority int32
		Flags                       uint32
	}
	entry.Size = uint32(unsafe.Sizeof(entry))
	err = jobCall(threadFirst, uintptr(snapshot), uintptr(unsafe.Pointer(&entry)))
	for err == nil {
		if entry.Owner == uint32(c.Process.Pid) {
			thread, _, err := openThread.Call(0x2, 0, uintptr(entry.ID)) // THREAD_SUSPEND_RESUME
			if thread == 0 {
				return err
			}
			count, _, resumeErr := resumeThread.Call(thread)
			closeErr := syscall.CloseHandle(syscall.Handle(thread))
			if uint32(count) == 0xffffffff {
				return errors.Join(resumeErr, closeErr)
			}
			if count != 1 {
				return errors.Join(fmt.Errorf("unexpected initial suspend count: %d", count), closeErr)
			}
			return closeErr
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
		err = jobCall(threadNext, uintptr(snapshot), uintptr(unsafe.Pointer(&entry)))
	}
	return fmt.Errorf("suspended process thread not found: %w", err)
}

func (c *windowsCommand) Wait() error {
	err := c.Cmd.Wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	if closeErr := c.closeJob(); closeErr != nil {
		return errors.Join(err, closeErr)
	}
	return err
}

func (c *windowsCommand) Run() error {
	if err := c.Start(); err != nil {
		return err
	}
	return c.Wait()
}

func (c *windowsCommand) Output() ([]byte, error) {
	if c.Stdout != nil {
		return nil, errors.New("exec: Stdout already set")
	}
	var out bytes.Buffer
	c.Stdout = &out
	err := c.Run()
	return out.Bytes(), err
}
