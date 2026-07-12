package orchestrator

import (
	"fmt"
	"time"

	"github.com/clishakehq/clishake/internal/adapter"
	"github.com/clishakehq/clishake/internal/domain"
	"github.com/clishakehq/clishake/internal/messaging"
)

// createApproval records an approval request parsed from agent output.
func (o *Orchestrator) createApproval(a *domain.Agent, ev adapter.ParsedEvent) {
	ap := &domain.Approval{
		ID:        domain.NewID("ap"),
		AgentName: a.Name,
		Action:    ev.Fields["action"],
		Reason:    ev.Text,
		Risk:      ev.Fields["risk"],
		State:     domain.ApprovalPending,
		CreatedAt: time.Now().UTC(),
	}
	if err := o.Store.SaveApproval(ap); err != nil {
		return
	}
	o.setStatus(a, domain.StatusAwaitingApproval, "requested approval "+ap.ID)
	o.emit(domain.EvApprovalRequested, a.Name, ap.ID, map[string]any{
		"action": ap.Action, "risk": ap.Risk, "reason": truncate(ap.Reason, 200),
	})
}

// Decide resolves a pending approval and notifies the requesting agent.
func (o *Orchestrator) Decide(approvalID string, grant bool) (*domain.Approval, error) {
	ap, err := o.Store.GetApproval(approvalID)
	if err != nil {
		return nil, err
	}
	if ap == nil {
		return nil, fmt.Errorf("no approval %q", approvalID)
	}
	if ap.State != domain.ApprovalPending {
		return nil, fmt.Errorf("approval %s already %s", approvalID, ap.State)
	}
	now := time.Now().UTC()
	ap.DecidedAt = &now
	verdict := "denied"
	evType := domain.EvApprovalDenied
	ap.State = domain.ApprovalDenied
	if grant {
		verdict = "approved"
		evType = domain.EvApprovalGranted
		ap.State = domain.ApprovalGranted
	}
	if err := o.Store.SaveApproval(ap); err != nil {
		return nil, err
	}
	o.emit(evType, domain.LeadSender, ap.ID, map[string]any{
		"agent": ap.AgentName, "action": ap.Action,
	})
	// Tell the agent; also nudge its status back to running on approval.
	body := fmt.Sprintf("approval %s: %s (%s)", ap.ID, verdict, ap.Action)
	_, _ = o.Send(domain.LeadSender, "@"+ap.AgentName, body, messaging.SendOpts{
		Type: domain.MsgApproval,
		Meta: map[string]string{"approval_id": ap.ID, "verdict": verdict},
	})
	if a, _ := o.Store.GetAgentByName(ap.AgentName); a != nil && a.Status == domain.StatusAwaitingApproval {
		o.setStatus(a, domain.StatusRunning, "approval decided: "+verdict)
	}
	return ap, nil
}
