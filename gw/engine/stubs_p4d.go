package engine

// Planners of the P4d paths not ported yet: each hands its requests to the control plane. Each stub goes away in the
// task that implements its planner.

func (s *State) PlanMessages(r *Request, now int64) Plan   { return handoff("not ported yet") }
func (s *State) PlanResponses(r *Request, now int64) Plan  { return handoff("not ported yet") }
func (s *State) PlanGemini(r *Request, now int64) Plan     { return handoff("not ported yet") }
func (s *State) PlanCount(r *Request, now int64) Plan      { return handoff("not ported yet") }
func (s *State) PlanEmbeddings(r *Request, now int64) Plan { return handoff("not ported yet") }

// geminiPathShape is geminiPath without decoding the model.
func geminiPathShape(raw string) (model, action string, ok bool) { return "", "", false }
