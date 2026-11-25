package monshi

import "github.com/monshihq/monshi/internal/engine"

// ViewResult is one consistent, permission-filtered reading of the case.
type ViewResult = engine.ViewResult

// ViewNode is a visible declaration or instance in the returned tree.
type ViewNode = engine.ViewNode

// ChangeResult reports the outcome of a working-copy update.
type ChangeResult = engine.ChangeResult

// CommitResult reports the outcome of submitting a form.
type CommitResult = engine.CommitResult

// TransitionResult reports the outcome of a stage transition.
type TransitionResult = engine.TransitionResult

// RebaseResult reports the outcome of merging committed changes into a draft.
type RebaseResult = engine.RebaseResult

// ReviewState is the effective decision on a verifiable unit.
type ReviewState = engine.ReviewState

const (
	ReviewPending  = engine.ReviewPending
	ReviewApproved = engine.ReviewApproved
	ReviewRejected = engine.ReviewRejected
)

// ReviewAction specifies approval, rejection, or revocation.
type ReviewAction = engine.ReviewAction

const (
	ReviewApprove = engine.ReviewApprove
	ReviewReject  = engine.ReviewReject
	ReviewRevoke  = engine.ReviewRevoke
)

// ReviewInfo describes the review decision governing a visible node.
type ReviewInfo = engine.ReviewInfo

// ReviewResult reports the outcome of a review.
type ReviewResult = engine.ReviewResult

// SignatureInfo describes an attestation on a signature field.
type SignatureInfo = engine.SignatureInfo

// SignResult reports the outcome of signing a field.
type SignResult = engine.SignResult

// HistoryResult holds a page of history and its continuation cursor.
type HistoryResult = engine.HistoryResult

// Commit is one revision of the case's audit trail.
type Commit = engine.Commit

// CommitOp identifies the operation that produced a commit.
type CommitOp = engine.CommitOp

const (
	CommitSubmit     = engine.CommitSubmit
	CommitTransition = engine.CommitTransition
	CommitReview     = engine.CommitReview
	CommitSign       = engine.CommitSign
)

// ChangeKind identifies the payload of a committed change.
type ChangeKind = engine.ChangeKind

const (
	ChangeValue  = engine.ChangeValue
	ChangeRows   = engine.ChangeRows
	ChangeReview = engine.ChangeReview
	ChangeSign   = engine.ChangeSign
)

// CommitChange records a change at a stable declaration or instance path.
type CommitChange = engine.CommitChange
