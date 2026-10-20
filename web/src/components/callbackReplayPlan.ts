import type { CallbackDelivery } from "../api/callbackDelivery";
import type { CommandPlan } from "../api/commandIntent";

// Only the exact content-free local tuple participates in this command. No
// endpoint, payload, credentials, receiver response or confirmation input.
export function callbackReplayPlan(delivery: CallbackDelivery): CommandPlan {
  return {
    kind: "callback_replay",
    datasets: [],
    callback: {
      subscription_id: delivery.subscription_id,
      delivery_id: delivery.id,
      event_id: delivery.event_id,
      revision: delivery.revision,
      lease_generation: delivery.lease_generation,
    },
  };
}
export function canonicalCallbackReplayPlan(plan: CommandPlan): CommandPlan {
  const target = plan.callback!;
  return {
    kind: "callback_replay",
    datasets: [],
    callback: {
      subscription_id: target.subscription_id.toLowerCase(),
      delivery_id: target.delivery_id.toLowerCase(),
      event_id: target.event_id.toLowerCase(),
      revision: target.revision,
      lease_generation: target.lease_generation,
    },
  };
}
