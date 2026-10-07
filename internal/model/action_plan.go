package model

import "context"

type ActionPlan struct {
	UpdateIDs []int64
	Actions   []Decision
	Completed int
}

type ActionPlanStore interface {
	LoadActionPlan(context.Context, int64, int64, int64) (ActionPlan, bool, error)
	SaveActionPlan(context.Context, int64, int64, int64, ActionPlan) (ActionPlan, error)
	MarkActionCompleted(context.Context, int64, int64, int64, int) error
}
