package main

import "sync"

// Process-scoped teardown, for fixtures that deliberately outlive the test
// that first asked for them.
//
// `t.Cleanup` and `t.TempDir` are scoped to ONE test. That is the right scope
// for almost everything, and the wrong scope for the eval suite's shared
// corpus: the whole point of building the repository index once is that the
// second and third tests still use it after the first has returned, at which
// point `t.TempDir`'s directory is already gone and a `t.Cleanup` has already
// stopped the embedder. So the shared fixture allocates with os.MkdirTemp and
// registers its unwinding here instead.
//
// TestMain drains this AFTER m.Run and BEFORE os.Exit, which is the only
// window that works -- see the comment on TestMain in helperproc_test.go for
// what happens to teardown that trusts `defer` in front of os.Exit.
var (
	processCleanupMu sync.Mutex
	processCleanups  []func()
)

// registerProcessCleanup adds f to the stack of functions run once every test
// in this package has finished.
func registerProcessCleanup(f func()) {
	processCleanupMu.Lock()
	defer processCleanupMu.Unlock()
	processCleanups = append(processCleanups, f)
}

// runProcessCleanups runs every registered cleanup in reverse registration
// order, the way defer would have. A panic in one cleanup must not skip the
// rest -- a leaked helper subprocess is a worse outcome than a noisy one --
// so each runs inside its own recover.
func runProcessCleanups() {
	processCleanupMu.Lock()
	fns := processCleanups
	processCleanups = nil
	processCleanupMu.Unlock()

	for i := len(fns) - 1; i >= 0; i-- {
		func() {
			defer func() { _ = recover() }()
			fns[i]()
		}()
	}
}
