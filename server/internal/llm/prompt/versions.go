package prompt

// DSLWorkflowVersion identifies the complete generation and repair prompt
// contract recorded on every durable DSL job.
const DSLWorkflowVersion = "dsl-workflow-v47"

// DSLSelectorRepairVersion identifies the bounded, one-call contract that may
// replace only server-owned opaque selector candidate slots.
const DSLSelectorRepairVersion = "dsl-selector-repair-v1"
