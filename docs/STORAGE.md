# Storage compatibility

Custom [`Storage`](../storage.go) implementations must implement `InitDraft`
atomically. Return the existing draft if present; otherwise compare the head
to `initial.Base` and insert revision zero or return `*Stale`. `Save` on a
missing draft returns `ErrNoDraft`.

New head, draft and history records use wire version 2. Each identifies its
record kind and definition layout. A field rename, enum reorder or stage
reorder is a schema mismatch, including after a restart. Changes limited to
permissions, defaults or decimal constraints do not invalidate the layout.

Version 1 records remain readable under their original layout, but contain
no layout identifier: register the original layout when reopening such data.
Writes upgrade individual records; history is append-only. Version 1 omitted
part or all of the decision/comment for some typed-field reviews. Reading
such an entry returns a storage error rather than inventing an audit record.
