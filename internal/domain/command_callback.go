package domain

// ValidCallbackReplayScope is shape validation, not authorization or evidence
// that the receiver did not execute an earlier delivery.
func (p CommandPlan) ValidCallbackReplayScope() bool {
	t := p.Callback
	return t != nil && t.SubscriptionID != (ID{}) && t.DeliveryID != (ID{}) && t.EventID != (ID{}) &&
		t.Revision > 0 && t.LeaseGeneration >= 0 && len(p.Datasets) == 0 && len(p.Steps) == 0 &&
		p.OperationType == "" && len(p.RequestPayload) == 0
}
