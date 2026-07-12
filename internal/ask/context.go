package ask

import (
	"fmt"

	"github.com/clishakehq/clishake/internal/domain"
)

// BuildContext formats raw session state into the compact SessionContext
// snapshot given to the translation model. Pure data transformation so both
// the CLI command and the dashboard build identical context.
func BuildContext(projectDir string, agents []*domain.Agent, tasks []*domain.Task, msgs []*domain.Message, adapters []string) SessionContext {
	sc := SessionContext{ProjectDir: projectDir, Adapters: adapters}
	for _, a := range agents {
		sc.Agents = append(sc.Agents, fmt.Sprintf("%s (role %s, adapter %s, status %s)",
			a.Name, a.Role, a.Adapter, a.Status))
	}
	for _, t := range tasks {
		owner := t.Owner
		if owner == "" {
			owner = "unassigned"
		}
		sc.Tasks = append(sc.Tasks, fmt.Sprintf("%s [%s→%s] %s", t.ID, t.Status, owner, t.Title))
	}
	for _, m := range msgs {
		body := m.Body
		if len(body) > 80 {
			body = body[:80] + "…"
		}
		sc.Messages = append(sc.Messages, fmt.Sprintf("%s → %s: %s", m.Sender, m.Recipient, body))
	}
	return sc
}
