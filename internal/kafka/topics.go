package kafka

// Run-plane and ingestion-plane topic names. Partition key is run_id on the
// run plane and repo_id on the ingestion plane.
const (
	TopicSpecReceived   = "spec.received"
	TopicRunRequested   = "run.requested"
	TopicStageRequested = "run.stage.requested"
	TopicStageCompleted = "run.stage.completed"
	TopicStageFailed    = "run.stage.failed"
	TopicDeliverPR      = "deliver.pr"
	TopicRunCompleted   = "run.completed"
	TopicRunGated       = "run.gated"
	TopicGateResume     = "gate.resume"
	TopicRunPROpened    = "run.pr.opened"
)

// DLQTopic returns the dead-letter topic name for a topic.
func DLQTopic(topic string) string { return topic + ".DLQ" }
