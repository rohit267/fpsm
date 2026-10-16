package main

import "sync"

// Where a present event came from. DXGI and D3D9 are the runtimes' own present calls; the kernel
// source sees every present whatever API made it (OpenGL, Vulkan, old APIs).
const (
	srcDXGI = iota
	srcD3D9
	srcKernel
	numSrc
)

const (
	maxStamps = 6000 // per process and source; ~1s of history even at 1000+ fps
	staleSecs = 3    // no presents for this long = not rendering
)

type procStat struct {
	ts [numSrc][]int64 // present timestamps (QPC ticks), oldest first
}

// statTable holds recent present timestamps per process. add runs on the ETW thread for every
// frame of every process on the box, so it stays cheap.
type statTable struct {
	mu   sync.Mutex
	freq int64
	m    map[uint32]*procStat
}

func newStatTable(freq int64) *statTable {
	return &statTable{freq: freq, m: make(map[uint32]*procStat)}
}

func (s *statTable) add(pid uint32, src int, t int64) {
	s.mu.Lock()
	ps := s.m[pid]
	if ps == nil {
		ps = &procStat{}
		s.m[pid] = ps
	}
	l := append(ps.ts[src], t)
	if len(l) > maxStamps {
		cut := 0
		for cut < len(l) && l[cut] < t-2*s.freq {
			cut++
		}
		if cut < len(l)/2 {
			cut = len(l) / 2
		}
		l = append(l[:0], l[cut:]...)
	}
	ps.ts[src] = l
	s.mu.Unlock()
}

// fps is the number of presents in the last second for pid. ok is false when the process has
// not presented recently. Event delivery lags a little, so the window ends at the newest event
// unless that is over a second old, in which case it ends now (a stall shows as a low number).
//
// DXGI and D3D9 counts are preferred because they are the app's own present calls. The kernel
// count wins only when it is well above them: a Vulkan or OpenGL game that also has a small DXGI
// overlay or launcher window should show the game's rate, not the overlay's.
func (s *statTable) fps(pid uint32, now int64) (fps int, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ps := s.m[pid]
	if ps == nil {
		return 0, false
	}
	var rate [numSrc]int
	var fresh [numSrc]bool
	for src, l := range ps.ts {
		if len(l) == 0 {
			continue
		}
		last := l[len(l)-1]
		age := now - last
		if age > staleSecs*s.freq {
			continue
		}
		fresh[src] = true
		ref := last
		if age > s.freq {
			ref = now
		}
		for i := len(l) - 1; i >= 0 && l[i] > ref-s.freq; i-- {
			rate[src]++
		}
	}
	api := max(rate[srcDXGI], rate[srcD3D9])
	apiFresh := fresh[srcDXGI] || fresh[srcD3D9]
	switch {
	case !apiFresh && !fresh[srcKernel]:
		return 0, false
	case !fresh[srcKernel]:
		return api, true
	case !apiFresh:
		return rate[srcKernel], true
	case api*10 >= rate[srcKernel]*4: // API count is at least 40% of the kernel's: it is the real one
		return api, true
	default:
		return rate[srcKernel], true
	}
}

// active lists every process that presented recently, with its frame rate.
func (s *statTable) active(now int64) map[uint32]int {
	s.mu.Lock()
	pids := make([]uint32, 0, len(s.m))
	for pid := range s.m {
		pids = append(pids, pid)
	}
	s.mu.Unlock()
	out := make(map[uint32]int)
	for _, pid := range pids {
		if f, ok := s.fps(pid, now); ok {
			out[pid] = f
		}
	}
	return out
}

// prune forgets processes that have been quiet for a while.
func (s *statTable) prune(now int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for pid, ps := range s.m {
		quiet := true
		for _, l := range ps.ts {
			if len(l) > 0 && now-l[len(l)-1] < 30*s.freq {
				quiet = false
			}
		}
		if quiet {
			delete(s.m, pid)
		}
	}
}
