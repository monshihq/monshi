// Package engine executes case operations against compiled programs.
// A frame owns the values being interpreted; authority comes from committed
// state even when the values being edited or displayed come from a draft.
//
// Operations use caseBackend to read snapshots and publish changes. The
// native backend works directly on snapshots; the host-storage adapter
// encodes the same snapshots and history for durable storage.
package engine
