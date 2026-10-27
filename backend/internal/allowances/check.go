package allowances

import (
	"fmt"
	"strings"
)

// Container is one of the user's containers as the check counts it. A limit of 0 means
// the container has none.
type Container struct {
	CPUsMilli   int64
	MemoryBytes int64
}

// Usage is what a user's containers and images take now.
type Usage struct {
	CPUsMilli   int64
	MemoryBytes int64
	DiskBytes   int64
	Containers  int64
}

// Tally adds up the limits of a user's containers, every one of them, running or not
// (a stopped container can start). One without a CPU or memory limit can take all of the
// machine, so it counts as the whole of a's.
func Tally(a Allowance, cs []Container, disk int64) Usage {
	u := Usage{DiskBytes: disk, Containers: int64(len(cs))}
	for _, c := range cs {
		u.CPUsMilli += wholeIfNone(c.CPUsMilli, a.CPUsMilli)
		u.MemoryBytes += wholeIfNone(c.MemoryBytes, a.MemoryBytes)
	}
	return u
}

func wholeIfNone(v, whole int64) int64 {
	if v <= 0 {
		return whole
	}
	return v
}

// Left is what a leaves the user after u, never below 0.
func Left(a Allowance, u Usage) Usage {
	return Usage{
		CPUsMilli:   max(a.CPUsMilli-u.CPUsMilli, 0),
		MemoryBytes: max(a.MemoryBytes-u.MemoryBytes, 0),
		DiskBytes:   max(a.DiskBytes-u.DiskBytes, 0),
		Containers:  max(a.MaxContainers-u.Containers, 0),
	}
}

// Request is the container a create, recreate or start asks for.
type Request struct {
	Image string
	// CPUsMilli and MemoryBytes are its limits; 0 means none, which is refused.
	CPUsMilli   int64
	MemoryBytes int64
	// Ports are its host ports, each from 1 to 65535 (pick a free one for a 0 with
	// FreePort first).
	Ports []int
	// Create is true for a new container (a create or recreate), which the disk allowance
	// refuses once it is used up; a start isn't refused for disk.
	Create bool
}

// ExceededError is a request that doesn't fit the allowance.
type ExceededError struct {
	// Resource is what is over: "cpus", "memory", "disk", "containers", "ports" or "image".
	Resource string
	msg      string
}

func (e *ExceededError) Error() string { return e.msg }

func exceeded(resource, format string, args ...any) *ExceededError {
	return &ExceededError{Resource: resource, msg: fmt.Sprintf(format, args...)}
}

// Check reports whether req fits a, with used being what the user's other containers
// (not the one req replaces or starts) and images take: an *ExceededError if not.
func Check(a Allowance, used Usage, req Request) error {
	if !a.ImageAllowed(req.Image) {
		return exceeded("image", "the image %s is not in your allowance's images", req.Image)
	}
	if used.Containers+1 > a.MaxContainers {
		return exceeded("containers", "your allowance is %d containers and you have %d", a.MaxContainers, used.Containers)
	}
	if req.CPUsMilli <= 0 {
		return exceeded("cpus", "the container needs a CPU limit")
	}
	if req.MemoryBytes <= 0 {
		return exceeded("memory", "the container needs a memory limit")
	}
	if used.CPUsMilli+req.CPUsMilli > a.CPUsMilli {
		return exceeded("cpus", "%s CPUs asked, %s left of your %s", cpus(req.CPUsMilli), cpus(max(a.CPUsMilli-used.CPUsMilli, 0)), cpus(a.CPUsMilli))
	}
	if used.MemoryBytes+req.MemoryBytes > a.MemoryBytes {
		return exceeded("memory", "%s of memory asked, %s left of your %s", Bytes(req.MemoryBytes), Bytes(max(a.MemoryBytes-used.MemoryBytes, 0)), Bytes(a.MemoryBytes))
	}
	if req.Create && used.DiskBytes >= a.DiskBytes {
		return exceeded("disk", "your containers, volumes and images use %s of your %s of disk", Bytes(used.DiskBytes), Bytes(a.DiskBytes))
	}
	for _, p := range req.Ports {
		if !a.Ports() || p < a.PortMin || p > a.PortMax {
			if !a.Ports() {
				return exceeded("ports", "your allowance has no host ports")
			}
			return exceeded("ports", "host port %d is outside your ports %d-%d", p, a.PortMin, a.PortMax)
		}
	}
	return nil
}

// CheckPull reports whether the user may pull image ref with used taken.
func CheckPull(a Allowance, used Usage, ref string) error {
	if !a.ImageAllowed(ref) {
		return exceeded("image", "the image %s is not in your allowance's images", ref)
	}
	if used.DiskBytes >= a.DiskBytes {
		return exceeded("disk", "your containers, volumes and images use %s of your %s of disk", Bytes(used.DiskBytes), Bytes(a.DiskBytes))
	}
	return nil
}

// CheckDisk reports whether used fits a's disk allowance (after a pull).
func CheckDisk(a Allowance, used Usage) error {
	if used.DiskBytes > a.DiskBytes {
		return exceeded("disk", "the image takes your disk use to %s, over your %s", Bytes(used.DiskBytes), Bytes(a.DiskBytes))
	}
	return nil
}

// FreePort returns the lowest port in a's range that isn't in taken, false if none is.
func FreePort(a Allowance, taken map[int]bool) (int, bool) {
	if !a.Ports() {
		return 0, false
	}
	for p := a.PortMin; p <= a.PortMax; p++ {
		if !taken[p] {
			return p, true
		}
	}
	return 0, false
}

func cpus(milli int64) string {
	s := fmt.Sprintf("%d.%03d", milli/1000, milli%1000)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// Bytes formats n in binary units ("512 MiB").
func Bytes(n int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	i, v := 0, float64(n)
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 || v == float64(int64(v)) {
		return fmt.Sprintf("%d %s", int64(v), units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}
