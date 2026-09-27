# Task priority

## Goal
Tasks carry a priority from 0 (none) to 3 (urgent), stored with the task and used to order the list.

## Behaviour
`model.Task.Priority` is validated (0-3). The file store writes it as `"priority"` and reads it back; a file without it loads as 0. `Handler.Add(title, priority)` stores it; `Handler.List()` orders by priority descending, then ID ascending.

## Scope
**In:** model, store and handler changes, and their tests. **Out:** changing priorities of existing tasks, a CLI.

## Edge cases
- A priority of 4 or -1 is rejected by Add, and nothing is stored.
- A task file written before this change still loads.

## Success criteria
- [ ] C1: Validate accepts 0-3 and rejects anything else -- check: go test ./model
- [ ] C2: priority survives a save and load, and an old file loads as 0 -- check: go test ./store
- [ ] C3: Add stores the priority and rejects an invalid one -- check: go test ./handler
- [ ] C4: List orders by priority, highest first, then by ID -- check: go test ./handler

## Tasks
- [ ] tests for C1-C4
- [ ] model.Task.Priority and Validate
- [ ] store record field
- [ ] Handler.Add signature and List order
