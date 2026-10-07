package fault

import (
	"fmt"
	"os"
	"sync"
)

const (
	WagerBeforeCommit               = "wager.before_commit"
	ConsumerAfterCommitBeforeDelete = "consumer.after_commit_before_delete"
	OutboxAfterClaimBeforePublish   = "outbox.after_claim_before_publish"
	OutboxAfterPublishBeforeMark    = "outbox.after_publish_before_mark"
	WorkerAfterClaim                = "worker.after_claim"
)

var active = sync.OnceValue(func() string {
	if os.Getenv("FAULT_INJECTION_ENABLED") != "true" {
		return ""
	}
	return os.Getenv("FAULT_POINT")
})

// Point kills the process, as a crash would, when fault injection is enabled
// and FAULT_POINT names this point. It does nothing otherwise.
func Point(name string) {
	if active() == name {
		fmt.Fprintf(os.Stderr, "fault injection: exiting at %s\n", name)
		os.Exit(137)
	}
}
