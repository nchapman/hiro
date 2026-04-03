package cluster

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/nchapman/hiro/internal/ipc/proto"
)

// --- Test harness ---

// syncPair wires two FileSyncService instances together via their sendFn
// callbacks, simulating a leader↔worker sync channel without gRPC.
type syncPair struct {
	leaderDir string
	workerDir string
	leader    *FileSyncService
	worker    *FileSyncService

	leaderSent atomic.Int64
	workerSent atomic.Int64

	// Injectable fault injection.
	dropRate atomic.Int64 // 0-100, percentage of leader→worker messages to drop
}

func newSyncPair(t *testing.T) *syncPair {
	t.Helper()
	leaderDir := t.TempDir()
	workerDir := t.TempDir()
	os.MkdirAll(filepath.Join(leaderDir, "workspace"), 0o755)
	os.MkdirAll(filepath.Join(workerDir, "workspace"), 0o755)

	p := &syncPair{leaderDir: leaderDir, workerDir: workerDir}

	p.leader = NewFileSyncService(FileSyncConfig{
		RootDir:  leaderDir,
		SyncDirs: []string{"workspace"},
		NodeID:   "leader",
		SendFn: func(update *pb.FileUpdate) error {
			p.leaderSent.Add(1)
			if dr := p.dropRate.Load(); dr > 0 && rand.Intn(100) < int(dr) {
				return nil
			}
			return p.worker.ApplyFileUpdate(update)
		},
	})
	p.worker = NewFileSyncService(FileSyncConfig{
		RootDir:  workerDir,
		SyncDirs: []string{"workspace"},
		NodeID:   "worker",
		SendFn: func(update *pb.FileUpdate) error {
			p.workerSent.Add(1)
			return p.leader.ApplyFileUpdate(update)
		},
	})

	return p
}

func (p *syncPair) startWatchers(t *testing.T) {
	t.Helper()
	go p.leader.WatchAndSync()
	go p.worker.WatchAndSync()
	t.Cleanup(func() {
		p.leader.Stop()
		p.worker.Stop()
	})
	// Give watchers time to register.
	time.Sleep(100 * time.Millisecond)
}

// waitFor polls condition until true or timeout, then fails the test.
func waitFor(t *testing.T, timeout time.Duration, condition func() bool, msgFmt string, args ...any) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf(msgFmt, args...)
}

// fileExists checks if a file exists with the expected content.
func fileExists(dir, relPath, wantContent string) bool {
	data, err := os.ReadFile(filepath.Join(dir, relPath))
	if err != nil {
		return false
	}
	return string(data) == wantContent
}

// countFiles counts regular files under dir (excluding .conflict and .hiro-tmp files).
func countFiles(dir string) int {
	count := 0
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if strings.Contains(base, ".conflict.") || strings.HasPrefix(base, ".hiro-tmp-") {
			return nil
		}
		count++
		return nil
	})
	return count
}

// countConflictFiles counts .conflict.* files under dir.
func countConflictFiles(dir string) int {
	count := 0
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.Contains(filepath.Base(path), ".conflict.") {
			count++
		}
		return nil
	})
	return count
}

// --- Tests ---

// TestStress_RapidBurstWrites creates 500 files on the leader and verifies
// they all propagate to the worker through the fsnotify→debounce→send pipeline.
func TestStress_RapidBurstWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	p := newSyncPair(t)
	p.startWatchers(t)

	const n = 500
	for i := range n {
		path := filepath.Join(p.leaderDir, "workspace", "burst", fmt.Sprintf("file-%03d.txt", i))
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, fmt.Appendf(nil, "content-%d", i), 0o644)
	}

	// Wait for all files to arrive on the worker.
	waitFor(t, 15*time.Second, func() bool {
		return countFiles(filepath.Join(p.workerDir, "workspace", "burst")) >= n
	}, "expected %d files on worker, got %d", n, countFiles(filepath.Join(p.workerDir, "workspace", "burst")))

	// Verify content of a sample of files.
	for _, i := range []int{0, 99, 250, 499} {
		rel := filepath.Join("workspace", "burst", fmt.Sprintf("file-%03d.txt", i))
		want := fmt.Sprintf("content-%d", i)
		if !fileExists(p.workerDir, rel, want) {
			got, _ := os.ReadFile(filepath.Join(p.workerDir, rel))
			t.Errorf("file-%03d.txt: got %q, want %q", i, string(got), want)
		}
	}
}

// TestStress_BidirectionalSync has both sides writing different files
// simultaneously and verifies convergence to the union of all files.
func TestStress_BidirectionalSync(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	p := newSyncPair(t)
	p.startWatchers(t)

	const n = 200
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := range n {
			path := filepath.Join(p.leaderDir, "workspace", "from-leader", fmt.Sprintf("file-%03d.txt", i))
			os.MkdirAll(filepath.Dir(path), 0o755)
			os.WriteFile(path, fmt.Appendf(nil, "leader-%d", i), 0o644)
		}
	}()

	go func() {
		defer wg.Done()
		for i := range n {
			path := filepath.Join(p.workerDir, "workspace", "from-worker", fmt.Sprintf("file-%03d.txt", i))
			os.MkdirAll(filepath.Dir(path), 0o755)
			os.WriteFile(path, fmt.Appendf(nil, "worker-%d", i), 0o644)
		}
	}()

	wg.Wait()

	// Both sides should eventually have all 400 files.
	waitFor(t, 20*time.Second, func() bool {
		leaderFromWorker := countFiles(filepath.Join(p.leaderDir, "workspace", "from-worker"))
		workerFromLeader := countFiles(filepath.Join(p.workerDir, "workspace", "from-leader"))
		return leaderFromWorker >= n && workerFromLeader >= n
	}, "convergence failed: leader has %d from-worker files, worker has %d from-leader files",
		countFiles(filepath.Join(p.leaderDir, "workspace", "from-worker")),
		countFiles(filepath.Join(p.workerDir, "workspace", "from-leader")))

	// Verify no conflicts (paths are disjoint).
	conflicts := countConflictFiles(p.leaderDir) + countConflictFiles(p.workerDir)
	if conflicts > 0 {
		t.Errorf("expected 0 conflict files, got %d", conflicts)
	}
}

// TestStress_AtomicWriteIntegrity has 50 goroutines writing distinct content
// to the same file via ApplyFileUpdate and verifies concurrent readers never
// see partial content.
func TestStress_AtomicWriteIntegrity(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "workspace"), 0o755)

	svc := NewFileSyncService(FileSyncConfig{
		RootDir:  dir,
		SyncDirs: []string{"workspace"},
		NodeID:   "test",
	})

	const (
		writers    = 50
		iterations = 20
		fileSize   = 4096
	)

	filePath := filepath.Join(dir, "workspace", "hotfile.txt")

	// Seed the file so readers don't get ENOENT on the first iteration.
	os.WriteFile(filePath, bytes.Repeat([]byte{0}, fileSize), 0o644)

	var violations atomic.Int64
	var readerDone atomic.Bool
	var readerWg sync.WaitGroup

	// Reader goroutine: continuously reads and checks for partial content.
	readerWg.Add(1)
	go func() {
		defer readerWg.Done()
		for !readerDone.Load() {
			data, err := os.ReadFile(filePath)
			if err != nil {
				continue
			}
			if len(data) != fileSize {
				violations.Add(1)
				continue
			}
			// Every byte should be the same value (homogeneous content).
			first := data[0]
			for _, b := range data[1:] {
				if b != first {
					violations.Add(1)
					break
				}
			}
		}
	}()

	// Writer goroutines.
	var writerWg sync.WaitGroup
	for w := range writers {
		writerWg.Add(1)
		go func(id int) {
			defer writerWg.Done()
			content := bytes.Repeat([]byte{byte(id)}, fileSize)
			for range iterations {
				svc.ApplyFileUpdate(&pb.FileUpdate{
					Path:           "workspace/hotfile.txt",
					Content:        content,
					Mode:           0o644,
					MtimeUnixNanos: time.Now().UnixNano(),
					OriginNode:     fmt.Sprintf("writer-%d", id),
				})
			}
		}(w)
	}

	writerWg.Wait()
	readerDone.Store(true)
	readerWg.Wait()

	if v := violations.Load(); v > 0 {
		t.Errorf("detected %d partial-read violations (expected 0)", v)
	}

	// Final content should be homogeneous.
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("reading final file: %v", err)
	}
	if len(data) != fileSize {
		t.Fatalf("final file size = %d, want %d", len(data), fileSize)
	}
	first := data[0]
	for i, b := range data[1:] {
		if b != first {
			t.Fatalf("final file has mixed content at byte %d: %d != %d", i+1, b, first)
		}
	}
}

// TestStress_ConflictDetection creates conflicts by modifying a file on the
// worker side while receiving updates from the leader, and verifies conflict
// files are created. Uses separate files to avoid conflict filename collisions
// (production uses time.Now().Unix() which has 1s resolution).
func TestStress_ConflictDetection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "workspace"), 0o755)

	svc := NewFileSyncService(FileSyncConfig{
		RootDir:  dir,
		SyncDirs: []string{"workspace"},
		NodeID:   "worker",
	})

	const iterations = 20

	// Test each conflict on a separate file to avoid filename collisions
	// from the 1-second-resolution timestamp in conflict file names.
	for i := range iterations {
		relPath := fmt.Sprintf("workspace/shared-%03d.txt", i)
		absPath := filepath.Join(dir, relPath)

		// Establish baseline with an initial sync.
		baseMtime := time.Now().Add(-1 * time.Hour)
		svc.ApplyFileUpdate(&pb.FileUpdate{
			Path:           relPath,
			Content:        []byte("initial"),
			Mode:           0o644,
			MtimeUnixNanos: baseMtime.UnixNano(),
			OriginNode:     "leader",
		})

		// Simulate a local edit (mtime = now, well past the baseline).
		os.WriteFile(absPath, []byte(fmt.Sprintf("local-%d", i)), 0o644)
		now := time.Now()
		os.Chtimes(absPath, now, now)

		// Incoming remote update with mtime between baseline and local.
		// Use baseMtime + 1s which is guaranteed < now.
		remoteMtime := baseMtime.Add(1 * time.Second)
		err := svc.ApplyFileUpdate(&pb.FileUpdate{
			Path:           relPath,
			Content:        []byte(fmt.Sprintf("remote-%d", i)),
			Mode:           0o644,
			MtimeUnixNanos: remoteMtime.UnixNano(),
			OriginNode:     "leader",
		})
		if err != nil {
			t.Fatalf("iteration %d: ApplyFileUpdate: %v", i, err)
		}

		// Local file should be preserved.
		content, _ := os.ReadFile(absPath)
		if string(content) != fmt.Sprintf("local-%d", i) {
			t.Errorf("iteration %d: local file should be preserved, got %q", i, string(content))
		}
	}

	// Each file should have exactly 1 conflict file.
	conflicts := countConflictFiles(filepath.Join(dir, "workspace"))
	if conflicts != iterations {
		t.Errorf("expected %d conflict files, got %d", iterations, conflicts)
	}
}

// TestStress_InitialSyncWithConcurrentModifications creates a snapshot while
// files are being actively modified, applies it while the destination has its
// own activity, then runs Reconcile to verify convergence.
func TestStress_InitialSyncWithConcurrentModifications(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	// Set up leader with 100 files.
	leaderDir := t.TempDir()
	os.MkdirAll(filepath.Join(leaderDir, "workspace"), 0o755)

	const n = 100
	for i := range n {
		path := filepath.Join(leaderDir, "workspace", fmt.Sprintf("file-%03d.txt", i))
		os.WriteFile(path, []byte(fmt.Sprintf("original-%d", i)), 0o644)
	}

	leader := NewFileSyncService(FileSyncConfig{
		RootDir:  leaderDir,
		SyncDirs: []string{"workspace"},
		NodeID:   "leader",
	})

	// Modifier goroutine: continuously mutates random files during snapshot.
	stop := make(chan struct{})
	var modifierWg sync.WaitGroup
	modifierWg.Add(1)
	go func() {
		defer modifierWg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				i := rand.Intn(n)
				path := filepath.Join(leaderDir, "workspace", fmt.Sprintf("file-%03d.txt", i))
				os.WriteFile(path, []byte(fmt.Sprintf("modified-%d-%d", i, time.Now().UnixNano())), 0o644)
				time.Sleep(time.Millisecond)
			}
		}
	}()

	// Create snapshot while modifications are happening.
	data, err := leader.CreateInitialSync()
	if err != nil {
		t.Fatalf("CreateInitialSync: %v", err)
	}
	close(stop)
	modifierWg.Wait()

	// Apply to worker dir (which also has some of its own files).
	workerDir := t.TempDir()
	os.MkdirAll(filepath.Join(workerDir, "workspace", "worker-only"), 0o755)
	for i := range 20 {
		os.WriteFile(filepath.Join(workerDir, "workspace", "worker-only", fmt.Sprintf("w-%d.txt", i)), []byte("mine"), 0o644)
	}

	worker := NewFileSyncService(FileSyncConfig{
		RootDir:  workerDir,
		SyncDirs: []string{"workspace"},
		NodeID:   "worker",
	})

	if err := worker.ApplyInitialSyncStream(bytes.NewReader(data)); err != nil {
		t.Fatalf("ApplyInitialSyncStream: %v", err)
	}

	// All leader files should exist on worker (content may be stale from snapshot).
	for i := range n {
		path := filepath.Join(workerDir, "workspace", fmt.Sprintf("file-%03d.txt", i))
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("file-%03d.txt missing on worker after initial sync", i)
		}
	}

	// Worker-only files should be untouched.
	for i := range 20 {
		path := filepath.Join(workerDir, "workspace", "worker-only", fmt.Sprintf("w-%d.txt", i))
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "mine" {
			t.Errorf("worker-only file w-%d.txt was disturbed", i)
		}
	}

	// No temp files should remain.
	var tmpFiles []string
	filepath.WalkDir(workerDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(filepath.Base(path), ".hiro-tmp-") {
			tmpFiles = append(tmpFiles, path)
		}
		return nil
	})
	if len(tmpFiles) > 0 {
		t.Errorf("temp files left behind: %v", tmpFiles)
	}

	// Reconcile should bring worker fully up to date with leader's current state.
	leader.sendFn = func(update *pb.FileUpdate) error {
		return worker.ApplyFileUpdate(update)
	}
	if err := leader.Reconcile(nil); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// After reconcile, every file should match.
	for i := range n {
		rel := fmt.Sprintf("file-%03d.txt", i)
		leaderContent, _ := os.ReadFile(filepath.Join(leaderDir, "workspace", rel))
		workerContent, _ := os.ReadFile(filepath.Join(workerDir, "workspace", rel))
		if string(leaderContent) != string(workerContent) {
			t.Errorf("%s: leader=%q worker=%q", rel, string(leaderContent), string(workerContent))
		}
	}
}

// TestStress_EchoSuppression writes 50 files on the leader and verifies
// the worker does not echo them back (no amplification loop).
func TestStress_EchoSuppression(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	p := newSyncPair(t)
	p.startWatchers(t)

	const n = 50
	for i := range n {
		path := filepath.Join(p.leaderDir, "workspace", "echo-test", fmt.Sprintf("file-%03d.txt", i))
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, fmt.Appendf(nil, "content-%d", i), 0o644)
	}

	// Wait for files to arrive on worker.
	waitFor(t, 10*time.Second, func() bool {
		return countFiles(filepath.Join(p.workerDir, "workspace", "echo-test")) >= n
	}, "files didn't propagate to worker")

	// Wait an extra 2s beyond echo suppression TTL to let any echoes fire.
	time.Sleep(2 * time.Second)

	sent := p.leaderSent.Load()
	echoed := p.workerSent.Load()

	t.Logf("leader sent %d messages, worker sent %d messages", sent, echoed)

	// Worker should have sent very few messages (ideally 0). If echo
	// suppression fails, workerSent ≈ leaderSent (amplification loop).
	if echoed > 5 {
		t.Errorf("echo amplification detected: worker sent %d messages (expected ≤5, leader sent %d)", echoed, sent)
	}
}

// TestStress_DeepDirectoryTree creates deeply nested directory trees in a
// burst and verifies scanNewDir catches files at all levels.
func TestStress_DeepDirectoryTree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	p := newSyncPair(t)
	p.startWatchers(t)

	const (
		trees = 10
		depth = 5
	)

	// Create deep trees with files at every level.
	var allFiles []string
	for tree := range trees {
		parts := []string{p.leaderDir, "workspace", fmt.Sprintf("tree-%d", tree)}
		for level := range depth {
			parts = append(parts, fmt.Sprintf("level-%d", level))
			dir := filepath.Join(parts...)
			os.MkdirAll(dir, 0o755)
			file := filepath.Join(dir, "leaf.txt")
			os.WriteFile(file, []byte(fmt.Sprintf("tree-%d-level-%d", tree, level)), 0o644)

			rel, _ := filepath.Rel(p.leaderDir, file)
			allFiles = append(allFiles, rel)
		}
	}

	t.Logf("created %d files across %d trees", len(allFiles), trees)

	// Wait for all files to arrive on worker.
	waitFor(t, 15*time.Second, func() bool {
		for _, rel := range allFiles {
			if _, err := os.Stat(filepath.Join(p.workerDir, rel)); err != nil {
				return false
			}
		}
		return true
	}, "not all deep tree files propagated to worker (expected %d)", len(allFiles))

	// Verify content.
	for _, rel := range allFiles {
		leaderContent, _ := os.ReadFile(filepath.Join(p.leaderDir, rel))
		workerContent, _ := os.ReadFile(filepath.Join(p.workerDir, rel))
		if string(leaderContent) != string(workerContent) {
			t.Errorf("%s: leader=%q worker=%q", rel, string(leaderContent), string(workerContent))
		}
	}
}

// TestStress_DeleteDuringSync creates files, waits for propagation, then
// rapidly deletes them and verifies the deletes propagate correctly.
func TestStress_DeleteDuringSync(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	p := newSyncPair(t)
	p.startWatchers(t)

	const n = 100

	// Create files.
	dir := filepath.Join(p.leaderDir, "workspace", "ephemeral")
	os.MkdirAll(dir, 0o755)
	for i := range n {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("file-%03d.txt", i)), []byte(fmt.Sprintf("doomed-%d", i)), 0o644)
	}

	// Wait for propagation.
	waitFor(t, 10*time.Second, func() bool {
		return countFiles(filepath.Join(p.workerDir, "workspace", "ephemeral")) >= n
	}, "files didn't propagate before deletion")

	// Rapidly delete all files.
	for i := range n {
		os.Remove(filepath.Join(dir, fmt.Sprintf("file-%03d.txt", i)))
	}

	// Wait for deletes to propagate.
	waitFor(t, 10*time.Second, func() bool {
		return countFiles(filepath.Join(p.workerDir, "workspace", "ephemeral")) == 0
	}, "expected 0 files after deletion, got %d",
		countFiles(filepath.Join(p.workerDir, "workspace", "ephemeral")))

	// No temp files should remain.
	var tmpFiles []string
	filepath.WalkDir(p.workerDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(filepath.Base(path), ".hiro-tmp-") {
			tmpFiles = append(tmpFiles, path)
		}
		return nil
	})
	if len(tmpFiles) > 0 {
		t.Errorf("temp files left behind: %v", tmpFiles)
	}
}

// TestStress_ReconnectionGap simulates a disconnect, modifies files on both
// sides during the gap, then uses Reconcile to verify convergence.
func TestStress_ReconnectionGap(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	p := newSyncPair(t)
	p.startWatchers(t)

	// Phase 1: Establish baseline — 50 files synced from leader to worker.
	const baseFiles = 50
	for i := range baseFiles {
		path := filepath.Join(p.leaderDir, "workspace", "shared", fmt.Sprintf("file-%03d.txt", i))
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte(fmt.Sprintf("base-%d", i)), 0o644)
	}
	waitFor(t, 10*time.Second, func() bool {
		return countFiles(filepath.Join(p.workerDir, "workspace", "shared")) >= baseFiles
	}, "baseline sync failed")

	// Phase 2: Simulate disconnect — drop all leader→worker messages.
	p.dropRate.Store(100)
	p.leader.Stop()
	time.Sleep(300 * time.Millisecond) // let watcher drain

	// Phase 3: Modify files during the gap.
	// Leader: add 30 new files.
	for i := range 30 {
		path := filepath.Join(p.leaderDir, "workspace", "shared", fmt.Sprintf("gap-new-%03d.txt", i))
		os.WriteFile(path, []byte(fmt.Sprintf("gap-new-%d", i)), 0o644)
	}
	// Leader: modify 10 existing files.
	for i := range 10 {
		path := filepath.Join(p.leaderDir, "workspace", "shared", fmt.Sprintf("file-%03d.txt", i))
		os.WriteFile(path, []byte(fmt.Sprintf("gap-modified-%d", i)), 0o644)
	}
	// Leader: delete 5 files.
	for i := 45; i < 50; i++ {
		os.Remove(filepath.Join(p.leaderDir, "workspace", "shared", fmt.Sprintf("file-%03d.txt", i)))
	}
	// Worker: add 20 files outside the sync directory (to verify they're
	// not disturbed by Reconcile, which is authoritative for the sync dir).
	os.MkdirAll(filepath.Join(p.workerDir, "local-data"), 0o755)
	for i := range 20 {
		os.WriteFile(filepath.Join(p.workerDir, "local-data", fmt.Sprintf("w-%03d.txt", i)), []byte("mine"), 0o644)
	}

	// Phase 4: Reconnect — build knownFiles from worker's current state, reconcile.
	p.dropRate.Store(0)

	knownFiles := make(map[string]int64)
	filepath.WalkDir(filepath.Join(p.workerDir, "workspace"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		relPath, _ := filepath.Rel(p.workerDir, path)
		if shouldIgnore(relPath) || strings.Contains(filepath.Base(path), ".conflict.") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		knownFiles[relPath] = info.ModTime().UnixNano()
		return nil
	})

	// Wire reconcile to send to worker.
	reconcileSvc := NewFileSyncService(FileSyncConfig{
		RootDir:  p.leaderDir,
		SyncDirs: []string{"workspace"},
		NodeID:   "leader",
		SendFn: func(update *pb.FileUpdate) error {
			return p.worker.ApplyFileUpdate(update)
		},
	})
	if err := reconcileSvc.Reconcile(knownFiles); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Phase 5: Verify convergence.
	// New files should exist on worker.
	for i := range 30 {
		rel := filepath.Join("workspace", "shared", fmt.Sprintf("gap-new-%03d.txt", i))
		want := fmt.Sprintf("gap-new-%d", i)
		if !fileExists(p.workerDir, rel, want) {
			got, _ := os.ReadFile(filepath.Join(p.workerDir, rel))
			t.Errorf("gap-new-%03d.txt: got %q, want %q", i, string(got), want)
		}
	}

	// Modified files should be updated.
	for i := range 10 {
		rel := filepath.Join("workspace", "shared", fmt.Sprintf("file-%03d.txt", i))
		want := fmt.Sprintf("gap-modified-%d", i)
		if !fileExists(p.workerDir, rel, want) {
			got, _ := os.ReadFile(filepath.Join(p.workerDir, rel))
			t.Errorf("file-%03d.txt: got %q, want %q", i, string(got), want)
		}
	}

	// Deleted files should be gone.
	for i := 45; i < 50; i++ {
		path := filepath.Join(p.workerDir, "workspace", "shared", fmt.Sprintf("file-%03d.txt", i))
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("file-%03d.txt should have been deleted", i)
		}
	}

	// Files outside the sync directory should be untouched.
	for i := range 20 {
		path := filepath.Join(p.workerDir, "local-data", fmt.Sprintf("w-%03d.txt", i))
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "mine" {
			t.Errorf("local-data file w-%03d.txt was disturbed", i)
		}
	}
}
