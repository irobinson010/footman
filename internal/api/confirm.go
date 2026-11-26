package api

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/irobinson010/footman/internal/permissions"
)

// Confirmations are the pending click confirmations, for confirm.list and
// confirm.answer. permissions.Broker implements it.
type Confirmations interface {
	Pending() []permissions.Pending
	Answer(id string, allow bool, how string) error
}

// ConfirmListResult is the result of confirm.list.
type ConfirmListResult struct {
	// Requests are waiting for an answer, oldest first.
	Requests []permissions.Pending `json:"requests"`
}

// ConfirmAnswerParams are the params of confirm.answer.
type ConfirmAnswerParams struct {
	ID string `json:"id"`
	// Allow is required: true allows the request, false refuses it.
	Allow *bool `json:"allow"`
}

// ConfirmAnswerResult is the result of confirm.answer.
type ConfirmAnswerResult struct {
	ID      string `json:"id"`
	Allowed bool   `json:"allowed"`
}

func (m *methods) confirmList(ctx context.Context, c *Conn, params json.RawMessage) (any, error) {
	if err := DecodeParams(params, &struct{}{}); err != nil {
		return nil, err
	}
	if m.d.Confirm == nil {
		return nil, Errorf(CodeUnavailable, "confirmations are not available")
	}
	return ConfirmListResult{Requests: m.d.Confirm.Pending()}, nil
}

// confirmAnswer answers a confirmation. A socket client is the owner at the
// keyboard (ADR-0025), so its answer counts as a click, even for a
// click-only request.
func (m *methods) confirmAnswer(ctx context.Context, c *Conn, params json.RawMessage) (any, error) {
	var p ConfirmAnswerParams
	if err := DecodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.ID == "" || p.Allow == nil {
		return nil, Errorf(CodeInvalidParams, "id and allow are required")
	}
	if m.d.Confirm == nil {
		return nil, Errorf(CodeUnavailable, "confirmations are not available")
	}
	if err := m.d.Confirm.Answer(p.ID, *p.Allow, permissions.HowClick); err != nil {
		if errors.Is(err, permissions.ErrNoRequest) {
			return nil, Errorf(CodeNotFound, "no pending confirmation %q (it may have been answered or expired)", p.ID)
		}
		return nil, err
	}
	return ConfirmAnswerResult{ID: p.ID, Allowed: *p.Allow}, nil
}
