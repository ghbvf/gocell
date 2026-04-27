package initialadmin

import "github.com/ghbvf/gocell/runtime/worker"

type ensureAdminResult struct {
	Cleaner worker.Worker
}

type sweepResult struct {
	Cleaner worker.Worker
}
